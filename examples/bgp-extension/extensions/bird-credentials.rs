// bird reads no password from a file, and its strings take no escapes: this writes each of the
// unit's credentials as a constant of bird's config, named as the credential, which bird.conf's
// password lines name. A name outside letters, digits, '.', '_' and '-', or a value with a quote
// or a line break, fails the unit. The constants go to a new file that replaces the old one, so
// bird never reads a file half written or emptied by a failed run.
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::process::exit;

const CONF: &str = "/run/bird/credentials.conf";

fn fail(message: String) -> ! {
    eprintln!("{message}");
    exit(1)
}

fn main() {
    let mut conf = String::new();
    if let Some(dir) = std::env::var_os("CREDENTIALS_DIRECTORY") {
        let entries = fs::read_dir(&dir).unwrap_or_else(|e| fail(format!("{dir:?}: {e}")));
        let mut files: Vec<_> = entries.map(|e| e.unwrap_or_else(|e| fail(e.to_string())).path()).collect();
        files.sort();
        for file in files {
            let name = file.file_name().unwrap_or_default().to_string_lossy().into_owned();
            let Some(key) = name.strip_prefix("secret_") else { continue };
            if key.is_empty() || !key.bytes().all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b)) {
                fail(format!("{name}: bird takes a credential name of letters, digits, '.', '_' and '-' alone"));
            }
            let value = fs::read_to_string(&file).unwrap_or_else(|e| fail(format!("{name}: {e}")));
            let value = value.trim_end_matches('\n');
            if value.contains(['"', '\n']) {
                fail(format!("{name}: bird takes no password with a quote or a line break"));
            }
            conf += &format!("define '{name}' = \"{value}\";\n");
        }
    }
    let tmp = format!("{CONF}.new");
    let written = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(&tmp)
        .and_then(|mut f| f.write_all(conf.as_bytes()))
        .and_then(|()| fs::rename(&tmp, CONF));
    if let Err(e) = written {
        let _ = fs::remove_file(&tmp);
        fail(format!("{CONF}: {e}"));
    }
}
