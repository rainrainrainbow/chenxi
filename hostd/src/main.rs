//! chenxi-hostd：晨曦唯一带副作用的进程。
//! 零第三方 crate，只用 std；四层权限在每次请求执行前重新校验。

mod audit;
mod capability;
mod json;

use audit::Audit;
use capability::{ExecCap, HostCapability, Mode, NetCap, ReadCap, WriteCap};
use json::{encode_flat, parse_flat, Flat};
use std::collections::HashMap;
use std::fs;
use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

const MAX_LINE: usize = 65536;

struct Pending {
    req: Flat,
    created: u64,
}

struct Server {
    caps: HashMap<String, Arc<dyn HostCapability>>,
    allowed: Vec<String>,
    ws: PathBuf,
    max_output: usize,
    timeout: Duration,
    audit: Audit,
    pending: Mutex<HashMap<String, Pending>>,
    seq: AtomicUsize,
    active: AtomicUsize,
    max_workers: usize,
}

impl Server {
    fn new(cfg: &Flat) -> Server {
        let ws = PathBuf::from(
            cfg.get("workspace")
                .cloned()
                .unwrap_or_else(|| "/var/lib/chenxi/workspace".into()),
        );
        let _ = fs::create_dir_all(&ws);
        let max_output = cfg
            .get("max_output_bytes")
            .and_then(|s| s.parse::<usize>().ok())
            .unwrap_or(65536);
        let timeout = Duration::from_secs(
            cfg.get("timeout_sec")
                .and_then(|s| s.parse::<u64>().ok())
                .unwrap_or(20),
        );
        let max_workers = cfg
            .get("workers")
            .and_then(|s| s.parse::<usize>().ok())
            .unwrap_or(2)
            .max(1);
        let audit = Audit::new(
            cfg.get("audit")
                .map(|s| s.as_str())
                .unwrap_or("/var/log/chenxi/audit.jsonl"),
        );
        let allowed: Vec<String> = cfg
            .get("caps")
            .map(|s| {
                s.split(',')
                    .map(|x| x.trim().to_string())
                    .filter(|x| !x.is_empty())
                    .collect()
            })
            .unwrap_or_else(|| vec!["read".into(), "exec".into(), "write".into(), "net".into()]);

        let mut caps: HashMap<String, Arc<dyn HostCapability>> = HashMap::new();
        caps.insert("read".into(), Arc::new(ReadCap));
        caps.insert("write".into(), Arc::new(WriteCap));
        caps.insert("exec".into(), Arc::new(ExecCap));
        caps.insert("net".into(), Arc::new(NetCap));

        Server {
            caps,
            allowed,
            ws,
            max_output,
            timeout,
            audit,
            pending: Mutex::new(HashMap::new()),
            seq: AtomicUsize::new(0),
            active: AtomicUsize::new(0),
            max_workers,
        }
    }

    fn reply(id: &str, ok: bool, data: &str, err: &str) -> Flat {
        let mut m = Flat::new();
        m.insert("id".into(), id.to_string());
        m.insert("ok".into(), ok.to_string());
        m.insert("data".into(), data.to_string());
        m.insert("error".into(), err.to_string());
        m
    }

    fn handle(&self, req: &Flat) -> Flat {
        let id = req.get("id").map(|s| s.as_str()).unwrap_or("");
        let op = req.get("op").map(|s| s.as_str()).unwrap_or("");
        match op {
            "ping" => return Server::reply(id, true, "pong", ""),
            "list" => return Server::reply(id, true, &self.allowed.join(","), ""),
            "approve" => {
                let token = req.get("arg").map(|s| s.as_str()).unwrap_or("");
                let taken = self
                    .pending
                    .lock()
                    .ok()
                    .and_then(|mut g| g.remove(token));
                return match taken {
                    Some(p) => {
                        let mut approved = p.req.clone();
                        approved.insert("approval".into(), "allow".into());
                        self.execute(&approved)
                    }
                    None => Server::reply(id, false, "", "unknown or expired approval token"),
                };
            }
            "read" | "write" | "exec" | "net" => {}
            _ => return Server::reply(id, false, "", "unknown op"),
        }

        self.execute(req)
    }

    // 所有执行路径（含 approve 回放）都必须经过这道并发闸，
    // 避免审批分支绕过 max_workers 限制。
    fn execute(&self, req: &Flat) -> Flat {
        let id = req.get("id").map(|s| s.as_str()).unwrap_or("");
        let n = self.active.fetch_add(1, Ordering::SeqCst);
        if n >= self.max_workers {
            self.active.fetch_sub(1, Ordering::SeqCst);
            return Server::reply(id, false, "", "hostd busy");
        }
        let out = self.dispatch(req);
        self.active.fetch_sub(1, Ordering::SeqCst);
        out
    }

    fn dispatch(&self, req: &Flat) -> Flat {
        let id = req.get("id").map(|s| s.as_str()).unwrap_or("");
        let cap_name = req.get("cap").map(|s| s.as_str()).unwrap_or("");
        let op = req.get("op").map(|s| s.as_str()).unwrap_or("");
        let arg = req.get("arg").map(|s| s.as_str()).unwrap_or("");
        let mode = Mode::parse(req.get("mode").map(|s| s.as_str()).unwrap_or("readonly"));
        let approval = req
            .get("approval")
            .map(|s| s.as_str())
            .unwrap_or("deny");

        // 第 3 层：用户审批（ask 生成一次性 token，不缓存授权结果）
        if approval == "deny" {
            self.audit.log(req, false, "denied by approval policy");
            return Server::reply(id, false, "", "denied by approval policy");
        }
        if approval == "ask" {
            let secs = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_secs();
            let token = format!("ap-{}-{}", secs, self.seq.fetch_add(1, Ordering::SeqCst));
            if let Ok(mut g) = self.pending.lock() {
                // 5 分钟未确认的 token 自动失效，防止 1 GB 机器上无限堆积。
                g.retain(|_, p| secs.saturating_sub(p.created) < 300);
                if g.len() > 64 {
                    g.clear();
                }
                g.insert(
                    token.clone(),
                    Pending {
                        req: req.clone(),
                        created: secs,
                    },
                );
            }
            self.audit.log(req, false, "awaiting approval");
            return Server::reply(id, false, &token, "approval_required");
        }

        // 第 1 层：Host 授权（白名单由 hostd.json 决定，Go 侧无法扩权）
        if !self.allowed.iter().any(|c| c.as_str() == cap_name) {
            self.audit.log(req, false, "capability not in host allowlist");
            return Server::reply(id, false, "", "capability not in host allowlist");
        }
        let cap = match self.caps.get(cap_name) {
            Some(c) => c,
            None => {
                self.audit.log(req, false, "no capability implementation");
                return Server::reply(id, false, "", "no capability implementation");
            }
        };
        if cap.name() != op {
            self.audit.log(req, false, "op/capability mismatch");
            return Server::reply(id, false, "", "op/capability mismatch");
        }

        // 第 2 层：AI 能力档位（readonly / workspace / full）
        if let Err(e) = cap.check(mode, arg, &self.ws) {
            self.audit.log(req, false, &e);
            return Server::reply(id, false, "", &e);
        }
        match cap.run(arg, &self.ws, self.max_output, self.timeout) {
            Ok(data) => {
                self.audit.log(req, true, "ok");
                Server::reply(id, true, &data, "")
            }
            Err(e) => {
                self.audit.log(req, false, &e);
                Server::reply(id, false, "", &e)
            }
        }
    }
}

fn serve_conn(stream: UnixStream, srv: Arc<Server>) {
    let mut reader = BufReader::new(&stream);
    let mut writer = &stream;
    loop {
        let mut buf: Vec<u8> = Vec::new();
        match reader.read_until(b'\n', &mut buf) {
            Ok(0) => return,
            Ok(_) => {}
            Err(_) => return,
        }
        if buf.len() > MAX_LINE {
            return;
        }
        let line = String::from_utf8_lossy(&buf);
        let resp = match parse_flat(&line) {
            Ok(req) => srv.handle(&req),
            Err(e) => Server::reply("", false, "", &e),
        };
        let mut payload = encode_flat(&resp).into_bytes();
        payload.push(b'\n');
        if writer.write_all(&payload).is_err() {
            return;
        }
    }
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let cfg_path = args
        .iter()
        .position(|a| a == "--config")
        .and_then(|i| args.get(i + 1))
        .cloned()
        .unwrap_or_else(|| "hostd.json".to_string());
    let text = match fs::read_to_string(&cfg_path) {
        Ok(t) => t,
        Err(e) => {
            eprintln!("读取配置 {} 失败：{}", cfg_path, e);
            std::process::exit(1);
        }
    };
    let cfg = match parse_flat(&text) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("解析配置失败：{}", e);
            std::process::exit(1);
        }
    };

    let socket = cfg
        .get("socket")
        .cloned()
        .unwrap_or_else(|| "/run/chenxi/hostd.sock".into());
    if let Some(parent) = std::path::Path::new(&socket).parent() {
        let _ = fs::create_dir_all(parent);
    }
    let _ = fs::remove_file(&socket);
    let listener = match UnixListener::bind(&socket) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("绑定 {} 失败：{}", socket, e);
            std::process::exit(1);
        }
    };
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&socket, fs::Permissions::from_mode(0o660));
    }

    let srv = Arc::new(Server::new(&cfg));
    // 连接数上限：1 GB 机器上不允许无界线程堆积；执行并发另由 Server.active 限制。
    let live = Arc::new(AtomicUsize::new(0));
    let max_conns: usize = 16;
    eprintln!(
        "chenxi-hostd ready：{}（能力白名单：{}，连接上限 {}）",
        socket,
        srv.allowed.join(","),
        max_conns
    );
    for stream in listener.incoming() {
        match stream {
            Ok(s) => {
                if live.load(Ordering::SeqCst) >= max_conns {
                    continue;
                }
                live.fetch_add(1, Ordering::SeqCst);
                let shared = Arc::clone(&srv);
                let counter = Arc::clone(&live);
                std::thread::spawn(move || {
                    serve_conn(s, shared);
                    counter.fetch_sub(1, Ordering::SeqCst);
                });
            }
            Err(_) => continue,
        }
    }
}
