# Temporary ring ownership. EOF/2s heartbeat loss/mute always restore firmware.
set -eu
root=${ALICE_LED_ROOT:-/sys/class/leds}
gpio=${ALICE_GPIO_FILE:-/sys/kernel/debug/gpio}
saved=''
restore() {
 trap - EXIT HUP INT TERM
 for record in $saved; do
  path=${record%%,*}; rest=${record#*,}; value=${rest%%,*}; trigger=${rest#*,}
  # A newer owner takes precedence.
  current=$(cat "$path/trigger" 2>/dev/null) || continue
  case "$current" in none|*'[none]'*)
   printf '%s\n' "$value" > "$path/brightness" || true
   printf '%s\n' "$trigger" > "$path/trigger" || true;;
  esac
 done
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
for channel in red green blue; do
 case $channel in red) value=$r;; green) value=$g;; blue) value=$b;; esac
 # Limit brightness to the normal voice indicator range.
 value=$((value * 160 / 255))
 i=0
 while test "$i" -lt 24; do
  path="$root/cucumber:$channel:ring$i"
  trigger=$(sed -n 's/.*\[\([^]]*\)\].*/\1/p' "$path/trigger")
  test "$trigger" = ledfb-ring || exit 0
  old=$(cat "$path/brightness")
  saved="$path,$old,$trigger $saved"
  printf 'none\n' > "$path/trigger"
  printf '%s\n' "$value" > "$path/brightness"
  i=$((i+1))
 done
done
while IFS= read -r -t 2 tick; do unmuted || exit 0; done
