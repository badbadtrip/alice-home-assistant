# Temporary ownership of Plum's three RGB zones. Never touches the clock.
set -eu
root=${ALICE_LED_ROOT:-/sys/class/leds}
gpio=${ALICE_GPIO_FILE:-/sys/kernel/debug/gpio}
saved=''
restore() {
 trap - EXIT HUP INT TERM
 for record in $saved; do
  path=${record%%,*}; rest=${record#*,}; value=${rest%%,*}; trigger=${rest#*,}
  IFS= read -r current < "$path/trigger" || continue
  case "$current" in none|*'[none]'*)
   printf '%s\n' "$value" > "$path/brightness" || true
   printf '%s\n' "$trigger" > "$path/trigger" || true;;
  esac
 done
}
unmuted() {
 line=$(grep 'mute-switch' "$gpio" 2>/dev/null) || return 1
 case "$line" in *'in  hi'*) return 0;; *) return 1;; esac
}
trap restore EXIT
trap 'exit 0' HUP INT TERM
unmuted || exit 0
color=$1
case "$color" in ??????) ;; *) exit 1;; esac
case "$color" in *[!0-9a-fA-F]*) exit 1;; esac
r=$((0x$(printf '%s' "$color" | cut -c1-2)))
g=$((0x$(printf '%s' "$color" | cut -c3-4)))
b=$((0x$(printf '%s' "$color" | cut -c5-6)))
for channel in red green blue; do
 for i in 1 2 3; do
  path="$root/plum:$channel:ring$i"
  test -d "$path" || exit 0
  IFS= read -r triggers < "$path/trigger"
  trigger=${triggers#*\[}; trigger=${trigger%%\]*}
  test "$trigger" = led_ring_fb || exit 0
  IFS= read -r old < "$path/brightness"
  saved="$path,$old,$trigger $saved"
 done
done
for record in $saved; do
 path=${record%%,*}; printf 'none\n' > "$path/trigger"
 printf '0\n' > "$path/brightness"
done
while IFS= read -r -t 2 level; do
 unmuted || exit 0
 case "$level" in ''|*[!0-9]*) exit 0;; esac
 test "$level" -le 100 || exit 0
 for record in $saved; do
  path=${record%%,*}; IFS= read -r current < "$path/trigger"
  case "$current" in none|*'[none]'*) ;; *) exit 0;; esac
 done
 red=$((r * 160 * level / 255 / 100))
 green=$((g * 160 * level / 255 / 100))
 blue=$((b * 160 * level / 255 / 100))
 for i in 1 2 3; do
  printf '%s\n' "$red" > "$root/plum:red:ring$i/brightness"
  printf '%s\n' "$green" > "$root/plum:green:ring$i/brightness"
  printf '%s\n' "$blue" > "$root/plum:blue:ring$i/brightness"
 done
done
