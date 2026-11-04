# Temporary ring ownership. EOF/2s heartbeat loss/mute always restore firmware.
set -eu
root=${ALICE_LED_ROOT:-/sys/class/leds}
gpio=${ALICE_GPIO_FILE:-/sys/kernel/debug/gpio}
saved=''
backlight=${ALICE_BACKLIGHT_FILE:-/sys/class/leds/cucumber:red:ring0/device/gcc}
dim_owned=0
last_dim=''
restore() {
 trap - EXIT HUP INT TERM
 # Hide partial RGB restoration as well as acquisition.
 if test "$dim_owned" = 1; then
  current_dim=''; IFS= read -r current_dim < "$backlight" || test -n "$current_dim" || true
  if test "$current_dim" = "$last_dim"; then printf '0\n' > "$backlight" || true; last_dim=0; else dim_owned=0; fi
 fi
 for record in $saved; do
  path=${record%%,*}; rest=${record#*,}; value=${rest%%,*}; trigger=${rest#*,}
  # A newer owner takes precedence.
  IFS= read -r current < "$path/trigger" || continue
  case "$current" in none|*'[none]'*)
   printf '%s\n' "$value" > "$path/brightness" || true
   printf '%s\n' "$trigger" > "$path/trigger" || true;;
  esac
 done
 if test "$dim_owned" = 1; then printf '%s\n' "$original_dim" > "$backlight" || true; fi
}
unmuted() {
 line=$(grep 'mute-switch' "$gpio" 2>/dev/null) || return 1
 case "$line" in *'in  hi ACTIVE LOW'*) return 0;; *) return 1;; esac
}
trap restore EXIT
trap 'exit 0' HUP INT TERM
unmuted || exit 0
# MIDI's 24-zone ring only. Never borrow logo/clock or unknown hardware.
test -d "$root/cucumber:red:ring23" || exit 0
color=$1
case "$color" in ??????) ;; *) exit 1;; esac
case "$color" in *[!0-9a-fA-F]*) exit 1;; esac
r=$((0x$(printf '%s' "$color" | cut -c1-2)))
g=$((0x$(printf '%s' "$color" | cut -c3-4)))
b=$((0x$(printf '%s' "$color" | cut -c5-6)))
# Snapshot with shell builtins; external processes per LED cause visible color sweeps.
for channel in red green blue; do
 i=0
 while test "$i" -lt 24; do
  path="$root/cucumber:$channel:ring$i"
  IFS= read -r triggers < "$path/trigger"
  trigger=${triggers#*\[}; trigger=${trigger%%\]*}
  test "$trigger" = ledfb-ring || exit 0
  IFS= read -r old < "$path/brightness"
  saved="$path,$old,$trigger $saved"
  i=$((i+1))
 done
done
# Borrow all channels first, then paint adjacent RGB channels together.
original_dim=''; IFS= read -r original_dim < "$backlight" || test -n "$original_dim"
case "$original_dim" in ''|*[!0-9]*) exit 0;; esac
test "$original_dim" -le 255 || exit 0
dim_owned=1; last_dim=0
printf '0\n' > "$backlight"
for record in $saved; do path=${record%%,*}; printf 'none\n' > "$path/trigger"; done
r=$((r * 160 / 255)); g=$((g * 160 / 255)); b=$((b * 160 / 255))
i=0
while test "$i" -lt 24; do
 printf '%s\n' "$r" > "$root/cucumber:red:ring$i/brightness"
 printf '%s\n' "$g" > "$root/cucumber:green:ring$i/brightness"
 printf '%s\n' "$b" > "$root/cucumber:blue:ring$i/brightness"
 i=$((i+1))
done
# Frames carry a percentage, never RGB changes. EOF/timeout is still fail-safe.
while IFS= read -r -t 2 level; do
 unmuted || exit 0
 case "$level" in ''|*[!0-9]*) exit 0;; esac
 test "$level" -le 100 || exit 0
 current_dim=''; IFS= read -r current_dim < "$backlight" || test -n "$current_dim"
 test "$current_dim" = "$last_dim" || exit 0
 last_dim=$((original_dim * level / 100))
 printf '%s\n' "$last_dim" > "$backlight"
done
