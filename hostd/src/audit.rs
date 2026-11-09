//! Append-only 审计：只在出口做「不记录参数原文」的收敛，不在写入时猜内容。

use crate::json::{encode_flat, Flat};
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::Path;
use std::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};

pub struct Audit {
    path: String,
    mu: Mutex<()>,
}

impl Audit {
    pub fn new(path: &str) -> Audit {
        if let Some(parent) = Path::new(path).parent() {
            let _ = fs::create_dir_all(parent);
        }
        Audit {
            path: path.to_string(),
            mu: Mutex::new(()),
        }
    }

    pub fn log(&self, req: &Flat, ok: bool, detail: &str) {
        let _guard = self.mu.lock();
        let ts = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        let mut m = Flat::new();
        m.insert("ts".into(), ts.to_string());
        for k in ["id", "op", "cap", "mode", "approval"] {
            if let Some(v) = req.get(k) {
                m.insert(k.to_string(), v.clone());
            }
        }
        // 只记参数长度：命令或路径可能含敏感信息，不落盘。
        m.insert(
            "arg_len".into(),
            req.get("arg").map(|v| v.len()).unwrap_or(0).to_string(),
        );
        m.insert("ok".into(), ok.to_string());
        m.insert("detail".into(), detail.chars().take(200).collect());
        if let Ok(mut f) = OpenOptions::new().create(true).append(true).open(&self.path) {
            let _ = f.write_all(encode_flat(&m).as_bytes());
            let _ = f.write_all(b"\n");
        }
    }
}
