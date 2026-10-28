#!/usr/bin/env bash
# Install/upgrade a complete local bundle as a systemd service or LaunchDaemon.
set -euo pipefail
die() { echo "$*" >&2; exit 1; }
[[ $(id -u) == 0 ]] || die 'Run this installer with sudo.'
bundle=$(cd "$(dirname "$0")" && pwd)
os=$(uname -s)
prefix=/usr/local/lib/home-images
root=''
port=8372
host=127.0.0.1
user=''
case "$os" in
 Linux) config=/etc/home-images/config.json; data=/var/lib/home-images; library=libturbojpeg.so ;;
 Darwin) config='/Library/Application Support/HomeImages/config.json'; data='/Library/Application Support/HomeImages/data'; library=libturbojpeg.dylib ;;
 *) die 'Only Linux and macOS are supported.' ;;
esac
# sudo may omit Homebrew from PATH. Prefer its installed interpreter on macOS.
python_path=$(type -P python3 || true)
if [[ "$os" == Darwin ]]; then
 for candidate in /opt/homebrew/bin/python3 /usr/local/bin/python3; do
  if [[ -x "$candidate" ]]; then python_path=$candidate; break; fi
 done
fi
[[ -n "$python_path" ]] || die 'Python 3 is required by the installer (apt install python3 / brew install python).'
python3() { "$python_path" "$@"; }
while [[ $# -gt 0 ]]; do
 case "$1" in
  --root) root=${2:?}; shift 2 ;; --prefix) prefix=${2:?}; shift 2 ;;
  --config) config=${2:?}; shift 2 ;; --data-dir) data=${2:?}; shift 2 ;;
  --user) user=${2:?}; shift 2 ;; --port) port=${2:?}; shift 2 ;;
  --host) host=${2:?}; shift 2 ;; *) die "Unknown option: $1" ;;
 esac
done
[[ "$port" =~ ^[0-9]+$ ]] || die 'Invalid port.'
port=$((10#$port))
((port>0 && port<65536)) || die 'Invalid port.'
for path in "$prefix" "$config" "$data"; do [[ "$path" == /* && "$path" != / ]] || die 'Install, config and data paths must be absolute and non-root.'; done
[[ ! -L "$prefix" ]] || die 'Installation directory cannot be a symlink.'
[[ ! -e "$prefix" || -f "$prefix/.home-images-install.json" ]] || die 'Existing installation directory is not managed by this installer.'
[[ -f "$bundle/home-images" && -f "$bundle/$library" && -f "$bundle/turbojpeg-licenses/LICENSE.md" ]] || die 'Incomplete bundle: binary, codec and licenses are required.'
"$bundle/home-images" --version
field() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$1" "$2"; }
if [[ -f "$prefix/.home-images-install.json" ]]; then
 old_config=$(field "$prefix/.home-images-install.json" config)
 old_user=$(field "$prefix/.home-images-install.json" user)
 [[ "$config" == "$old_config" ]] || die 'Use the existing config path when upgrading.'
 [[ -z "$user" || "$user" == "$old_user" ]] || die 'Changing the service account requires a separate migration.'
 user=$old_user
fi
if [[ -z "$user" ]]; then
 if [[ "$os" == Linux ]]; then user=homeimages; else user=${SUDO_USER:-$(id -un)}; fi
fi
[[ "$user" != -* ]] || die 'Invalid service account.'
if [[ ! -f "$config" ]]; then
 [[ "$root" == /* && -d "$root" ]] || die '--root must name an existing absolute photo directory for a new installation.'
 mkdir -p "$(dirname "$config")"
 python3 - "$config" "$root" "$data" "$host" "$port" <<'PY'
import json,sys
with open(sys.argv[1],'x') as f:
    json.dump(dict(root=sys.argv[2],data_dir=sys.argv[3],host=sys.argv[4],port=int(sys.argv[5]),scan_interval_seconds=900,max_pixels=80000000,jpeg_decoder='auto',ffmpeg='ffmpeg',ml=dict(enabled=False)),f,indent=2)
PY
fi
root=$(field "$config" root)
data=$(field "$config" data_dir)
port=$(field "$config" port)
decoder=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("jpeg_decoder","auto"))' "$config")
health_host=$(python3 -c 'import json,sys; h=json.load(open(sys.argv[1])).get("host","127.0.0.1"); h="127.0.0.1" if h in ("","0.0.0.0") else "::1" if h=="::" else h; print("["+h+"]" if ":" in h else h)' "$config")
[[ "$root" == /* && -d "$root" && "$data" == /* && "$data" != / ]] || die 'Invalid existing photo/data paths.'
# Resolve boundaries before moving an existing installation. Data, config and
# originals must never be stored inside the replaceable program directory.
python3 - "$prefix" "$config" "$data" "$root" <<'PY'
import os,sys
p,c,d,r=map(os.path.realpath,sys.argv[1:])
if p in ('/','/usr','/usr/local','/usr/local/lib','/opt','/etc') or any(os.path.commonpath([p,x])==p for x in (c,d,r)) or os.path.commonpath([p,d])==d or os.path.commonpath([p,r])==r:
    raise SystemExit('Installation must be separate from config, data and originals.')
PY
if ! id "$user" >/dev/null 2>&1; then
 [[ "$os" == Linux ]] || die "Account does not exist: $user"
 useradd --system --home "$data" --shell /usr/sbin/nologin "$user"
fi
sudo -u "$user" test -r "$root" || die "Grant $user read/traverse access to the photo directory first; originals are not modified by this installer."
mkdir -p "$data" "$(dirname "$prefix")"
chown "$user:$(id -gn "$user")" "$data"
chmod 750 "$data"
chown "root:$(id -gn "$user")" "$config"
chmod 640 "$config"
if [[ ! -f "$prefix/.home-images-install.json" ]] && "$bundle/home-images" --config "$config" --service status >/dev/null 2>&1; then
 die 'An existing unmanaged HomeImages service must be migrated or uninstalled first.'
fi
stage=$(mktemp -d "${prefix}.new.XXXXXX")
backup="${prefix}.backup.$(date +%Y%m%d%H%M%S).$$"
installed=false
moved=false
old_stopped=false
old_uninstalled=false
cleanup() {
 code=$?
 if [[ $code != 0 ]]; then
  if $installed; then "$prefix/home-images" --config "$config" --service stop >/dev/null 2>&1 || true; "$prefix/home-images" --config "$config" --service uninstall >/dev/null 2>&1 || true; mv "$prefix" "${prefix}.failed.$(date +%Y%m%d%H%M%S).$$"; fi
  if $moved; then
   mv "$backup" "$prefix"
   "$prefix/home-images" --config "$config" --user "$user" --service install
   "$prefix/home-images" --config "$config" --service start
  elif $old_stopped; then
   if $old_uninstalled; then "$prefix/home-images" --config "$config" --user "$user" --service install; fi
   "$prefix/home-images" --config "$config" --service start
  fi
 fi
 [[ ! -d "$stage" ]] || rm -rf "$stage"
}
trap cleanup EXIT
chmod 755 "$stage"
install -m 755 "$bundle/home-images" "$stage/home-images"
install -m 755 "$bundle/$library" "$stage/$library"
cp -R "$bundle/turbojpeg-licenses" "$stage/turbojpeg-licenses"
cp -R "$bundle/third-party-licenses" "$stage/third-party-licenses"
cp "$bundle/LICENSE" "$bundle/THIRD-PARTY-NOTICES.md" "$stage/"
python3 - "$stage/.home-images-install.json" "$config" "$user" <<'PY'
import json,sys
with open(sys.argv[1],'w') as f: json.dump(dict(config=sys.argv[2],user=sys.argv[3]),f)
PY
if [[ -f "$prefix/.home-images-install.json" ]]; then
 "$prefix/home-images" --config "$config" --service stop
 old_stopped=true
 "$prefix/home-images" --config "$config" --service uninstall
 old_uninstalled=true
 mv "$prefix" "$backup"
 moved=true
fi
mv "$stage" "$prefix"
installed=true
"$prefix/home-images" --config "$config" --user "$user" --service install
"$prefix/home-images" --config "$config" --service start
ready=false
for ((attempt=0;attempt<60;attempt++)); do
 if response=$(curl --silent --fail --noproxy '*' --max-time 2 "http://$health_host:$port/health") && python3 -c 'import json,sys; h=json.loads(sys.argv[1]); assert h["status"]=="ok" and h["root_available"]; assert sys.argv[2]=="go" or h["jpeg_decoder"]=="turbojpeg"' "$response" "$decoder" 2>/dev/null; then ready=true; break; fi
 sleep 1
done
$ready || die 'Service did not become healthy with the configured decoder; restoring previous installation.'
echo "Installed $prefix/home-images; config preserved at $config"
if $moved; then echo "Previous program and codec retained at $backup"; fi
echo "Health: http://$health_host:$port/health"
