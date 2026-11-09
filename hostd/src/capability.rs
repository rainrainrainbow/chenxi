//! Operit2 式 Host 能力边界：子能力只通过 trait 暴露，
//! 父级（main.rs）负责装配与授权，能力自身不持有提权路径。

use std::fs;
use std::io::Read;
use std::net::{TcpStream, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    ReadOnly,
    Workspace,
    Full,
}

impl Mode {
    pub fn parse(s: &str) -> Mode {
        match s {
            "workspace" => Mode::Workspace,
            "full" => Mode::Full,
            _ => Mode::ReadOnly,
        }
    }
}

pub trait HostCapability: Send + Sync {
    fn name(&self) -> &'static str;
    fn check(&self, mode: Mode, arg: &str, ws: &Path) -> Result<(), String>;
    fn run(&self, arg: &str, ws: &Path, limit: usize, timeout: Duration) -> Result<String, String>;
}

fn safe_path(arg: &str, ws: &Path) -> Result<PathBuf, String> {
    if arg.is_empty() {
        return Err("empty path".into());
    }
    if arg.contains("..") || arg.contains('\0') {
        return Err("path traversal rejected".into());
    }
    let p = Path::new(arg);
    if p.is_absolute() {
        let ws_s = ws.to_string_lossy().to_string();
        let allowed = arg.starts_with("/proc/")
            || arg.starts_with("/sys/")
            || arg == "/etc/hostname"
            || arg.starts_with(&ws_s);
        if !allowed {
            return Err("absolute path outside host allowlist".into());
        }
        Ok(p.to_path_buf())
    } else {
        Ok(ws.join(p))
    }
}

fn bounded(bytes: &[u8], limit: usize) -> String {
    let n = bytes.len().min(limit);
    let mut s = String::from_utf8_lossy(&bytes[..n]).to_string();
    if bytes.len() > limit {
        s.push_str("\n[输出已按预算截断]");
    }
    s
}

pub struct ReadCap;

impl HostCapability for ReadCap {
    fn name(&self) -> &'static str {
        "read"
    }
    fn check(&self, _mode: Mode, arg: &str, ws: &Path) -> Result<(), String> {
        let p = safe_path(arg, ws)?;
        if !p.exists() {
            return Err("path not found".into());
        }
        Ok(())
    }
    fn run(&self, arg: &str, ws: &Path, limit: usize, _timeout: Duration) -> Result<String, String> {
        let p = safe_path(arg, ws)?;
        let mut f = fs::File::open(&p).map_err(|e| e.to_string())?;
        let mut buf = vec![0u8; limit];
        let n = f.read(&mut buf).map_err(|e| e.to_string())?;
        Ok(bounded(&buf[..n], limit))
    }
}

pub struct WriteCap;

fn split_write(arg: &str) -> Result<(String, String), String> {
    let (p, c) = arg
        .split_once('=')
        .ok_or("write arg must be relpath=content")?;
    if p.is_empty() {
        return Err("empty relpath".into());
    }
    Ok((p.to_string(), c.to_string()))
}

impl HostCapability for WriteCap {
    fn name(&self) -> &'static str {
        "write"
    }
    fn check(&self, mode: Mode, arg: &str, ws: &Path) -> Result<(), String> {
        if mode == Mode::ReadOnly {
            return Err("write denied in readonly mode".into());
        }
        let (p, content) = split_write(arg)?;
        if content.len() > 1_000_000 {
            return Err("content too large".into());
        }
        let path = safe_path(&p, ws)?;
        let ws_s = ws.to_string_lossy().to_string();
        if !path.to_string_lossy().starts_with(&ws_s) {
            return Err("write outside workspace".into());
        }
        Ok(())
    }
    fn run(&self, arg: &str, ws: &Path, limit: usize, _timeout: Duration) -> Result<String, String> {
        let (p, content) = split_write(arg)?;
        let path = safe_path(&p, ws)?;
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).map_err(|e| e.to_string())?;
        }
        let data: String = content.chars().take(limit).collect();
        fs::write(&path, data.as_bytes()).map_err(|e| e.to_string())?;
        Ok(format!("已写入 {} 字节到 {}", data.len(), path.display()))
    }
}

pub struct ExecCap;

const ALLOWED_COMMANDS: &[&str] = &[
    "uname", "free", "df", "uptime", "date", "hostname", "id", "whoami", "ls", "cat", "ps", "head",
    "wc", "grep", "find", "du", "systemctl", "journalctl", "ip",
];

fn shellish(c: char) -> bool {
    matches!(
        c,
        ';' | '|' | '&' | '$' | '>' | '<' | '`' | '!' | '(' | ')' | '{' | '}' | '[' | ']' | '\n'
            | '\r' | '\\' | '"' | '\''
    )
}

impl HostCapability for ExecCap {
    fn name(&self) -> &'static str {
        "exec"
    }
    fn check(&self, _mode: Mode, arg: &str, _ws: &Path) -> Result<(), String> {
        if arg.chars().any(shellish) {
            return Err("shell metacharacters rejected".into());
        }
        let parts: Vec<&str> = arg.split_whitespace().collect();
        if parts.is_empty() {
            return Err("empty command".into());
        }
        if !ALLOWED_COMMANDS.contains(&parts[0]) {
            return Err(format!("command not allowlisted: {}", parts[0]));
        }
        if parts.len() > 12 {
            return Err("too many arguments".into());
        }
        Ok(())
    }
    fn run(&self, arg: &str, _ws: &Path, limit: usize, timeout: Duration) -> Result<String, String> {
        let parts: Vec<&str> = arg.split_whitespace().collect();
        // 不经 shell：Command::new(prog).args(...) 本身就不会解释元字符。
        let mut child = Command::new(parts[0])
            .args(&parts[1..])
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|e| e.to_string())?;
        let deadline = Instant::now() + timeout;
        loop {
            match child.try_wait() {
                Ok(Some(_)) => break,
                Ok(None) => {
                    if Instant::now() >= deadline {
                        let _ = child.kill();
                        let _ = child.wait();
                        return Err("command timed out".into());
                    }
                    std::thread::sleep(Duration::from_millis(20));
                }
                Err(e) => return Err(e.to_string()),
            }
        }
        let out = child.wait_with_output().map_err(|e| e.to_string())?;
        let mut merged = Vec::with_capacity(out.stdout.len() + out.stderr.len());
        merged.extend_from_slice(&out.stdout);
        if !out.stderr.is_empty() {
            merged.extend_from_slice(b"\n[stderr]\n");
            merged.extend_from_slice(&out.stderr);
        }
        Ok(bounded(&merged, limit))
    }
}

pub struct NetCap;

impl HostCapability for NetCap {
    fn name(&self) -> &'static str {
        "net"
    }
    fn check(&self, _mode: Mode, arg: &str, _ws: &Path) -> Result<(), String> {
        if arg.contains('/') || arg.contains(' ') {
            return Err("arg must be host:port".into());
        }
        let (host, port) = arg.rsplit_once(':').ok_or("arg must be host:port")?;
        if host.is_empty() {
            return Err("empty host".into());
        }
        let p: u16 = port.parse().map_err(|_| "bad port")?;
        if p == 0 {
            return Err("port 0 not allowed".into());
        }
        Ok(())
    }
    fn run(&self, arg: &str, _ws: &Path, limit: usize, timeout: Duration) -> Result<String, String> {
        let addrs = arg.to_socket_addrs().map_err(|e| e.to_string())?;
        let mut last = "no resolvable address".to_string();
        for a in addrs {
            match TcpStream::connect_timeout(&a, timeout) {
                Ok(_) => return Ok(bounded(format!("TCP 连通：{}", a).as_bytes(), limit)),
                Err(e) => last = e.to_string(),
            }
        }
        Err(last)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_traversal_and_shell() {
        let ws = Path::new("/tmp/chenxi-test-ws");
        assert!(safe_path("../etc/passwd", ws).is_err());
        assert!(ExecCap
            .check(Mode::ReadOnly, "uname -a; rm -rf /", ws)
            .is_err());
        assert!(ExecCap.check(Mode::ReadOnly, "uname -a", ws).is_ok());
    }

    #[test]
    fn write_requires_non_readonly() {
        let ws = Path::new("/tmp/chenxi-test-ws");
        assert!(WriteCap
            .check(Mode::ReadOnly, "a.txt=hello", ws)
            .is_err());
        assert!(WriteCap
            .check(Mode::Workspace, "a.txt=hello", ws)
            .is_ok());
    }
}
