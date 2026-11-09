//! 极简「扁平字符串对象」JSON 解析/序列化。
//! 协议里所有值都是字符串，因此不需要 serde，也不需要数字/数组/嵌套解析。

use std::collections::BTreeMap;

pub type Flat = BTreeMap<String, String>;

fn skip_ws(b: &[u8], i: &mut usize) {
    while *i < b.len() && matches!(b[*i], b' ' | b'\t' | b'\r' | b'\n') {
        *i += 1;
    }
}

fn parse_string(b: &[u8], i: &mut usize) -> Result<String, String> {
    skip_ws(b, i);
    if *i >= b.len() || b[*i] != b'"' {
        return Err("expected string".into());
    }
    *i += 1;
    let mut out = String::new();
    let mut start = *i;
    loop {
        if *i >= b.len() {
            return Err("unterminated string".into());
        }
        let c = b[*i];
        if c == b'"' {
            let chunk = std::str::from_utf8(&b[start..*i]).map_err(|_| "invalid utf-8")?;
            out.push_str(chunk);
            *i += 1;
            return Ok(out);
        }
        if c == b'\\' {
            let chunk = std::str::from_utf8(&b[start..*i]).map_err(|_| "invalid utf-8")?;
            out.push_str(chunk);
            *i += 1;
            if *i >= b.len() {
                return Err("bad escape".into());
            }
            let e = b[*i];
            *i += 1;
            match e {
                b'"' => out.push('"'),
                b'\\' => out.push('\\'),
                b'/' => out.push('/'),
                b'n' => out.push('\n'),
                b'r' => out.push('\r'),
                b't' => out.push('\t'),
                b'u' => {
                    if *i + 4 > b.len() {
                        return Err("bad \\u escape".into());
                    }
                    let hex = std::str::from_utf8(&b[*i..*i + 4]).unwrap_or("");
                    *i += 4;
                    match u32::from_str_radix(hex, 16) {
                        Ok(cp) => out.push(char::from_u32(cp).unwrap_or('\u{FFFD}')),
                        Err(_) => out.push('\u{FFFD}'),
                    }
                }
                _ => return Err("unknown escape".into()),
            }
            start = *i;
            continue;
        }
        // 普通字节（含 UTF-8 多字节）先累积，遇到引号/转义时整段转字符串。
        *i += 1;
    }
}

pub fn parse_flat(line: &str) -> Result<Flat, String> {
    let b = line.as_bytes();
    let mut i = 0usize;
    skip_ws(b, &mut i);
    if i >= b.len() || b[i] != b'{' {
        return Err("expected '{'".into());
    }
    i += 1;
    let mut m = Flat::new();
    loop {
        skip_ws(b, &mut i);
        if i >= b.len() {
            return Err("unexpected end of object".into());
        }
        if b[i] == b'}' {
            break;
        }
        let key = parse_string(b, &mut i)?;
        skip_ws(b, &mut i);
        if i >= b.len() || b[i] != b':' {
            return Err("expected ':'".into());
        }
        i += 1;
        skip_ws(b, &mut i);
        let val = parse_string(b, &mut i)?;
        m.insert(key, val);
        skip_ws(b, &mut i);
        if i >= b.len() {
            return Err("unexpected end of object".into());
        }
        match b[i] {
            b',' => i += 1,
            b'}' => break,
            _ => return Err("expected ',' or '}'".into()),
        }
    }
    Ok(m)
}

pub fn escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 8);
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out
}

pub fn encode_flat(m: &Flat) -> String {
    let mut s = String::from("{");
    let mut first = true;
    for (k, v) in m {
        if !first {
            s.push(',');
        }
        first = false;
        s.push('"');
        s.push_str(&escape(k));
        s.push_str("\":\"");
        s.push_str(&escape(v));
        s.push('"');
    }
    s.push('}');
    s
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip_utf8_and_escapes() {
        let mut m = Flat::new();
        m.insert("arg".into(), "uname -a\n中文 \"引号\"".into());
        m.insert("ok".into(), "true".into());
        let line = encode_flat(&m);
        let back = parse_flat(&line).unwrap();
        assert_eq!(back.get("arg").unwrap(), "uname -a\n中文 \"引号\"");
        assert_eq!(back.get("ok").unwrap(), "true");
    }
}
