# Temporary ownership of Plum's three RGB zones. Never touches the clock.
set -eu
root=${ALICE_LED_ROOT:-/sys/class/leds}
gpio=${ALICE_GPIO_FILE:-/sys/kernel/debug/gpio}
saved=''; painted=''; acquired=0; changed=0
frame() {
 values=''
 for zone in 1 2 3; do
  for channel in red green blue; do
   IFS= read -r value < "$root/plum:$channel:ring$zone/brightness" || return 1
   values="$values $value"
  done
 done
 printf '%s' "$values"
}
restore() {
 trap - EXIT HUP INT TERM
 test "$acquired" = 1 || return 0
 # Never overwrite a frame changed by a newer writer, even if it uses 'none'.
 if test -n "$painted"; then
  current_frame=$(frame) || current_frame='unknown'
  test "$current_frame" = "$painted" || changed=1
 fi
 for record in $saved; do
  path=${record%%,*}; rest=${record#*,}; value=${rest%%,*}; trigger=${rest#*,}
  IFS= read -r current < "$path/trigger" || continue
  case "$current" in none|*'[none]'*)
   if test "$changed" = 0; then printf '%s\n' "$value" > "$path/brightness" || true; fi
   printf '%s\n' "$trigger" > "$path/trigger" || true;;
  esac
 done
}
unmuted() {
 while IFS= read -r line; do
  case "$line" in *'|mute-switch '*)
   case "$line" in *'in  hi'*) return 0;; *) return 1;; esac;;
  esac
 done < "$gpio"
 return 1
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
# Do not cover an existing all-red firmware warning.
set -- $(frame)
warning=1
for zone in 1 2 3; do
 if test "$1" -eq 0 || test "$2" -ne 0 || test "$3" -ne 0; then warning=0; fi
 shift 3
done
test "$warning" = 0 || exit 0
acquired=1
for record in $saved; do
 path=${record%%,*}; printf 'none\n' > "$path/trigger"
 printf '0\n' > "$path/brightness"
done
painted=' 0 0 0 0 0 0 0 0 0'
while IFS= read -r -t 2 command; do
 unmuted || exit 0
 for record in $saved; do
  path=${record%%,*}; IFS= read -r current < "$path/trigger"
  case "$current" in none|*'[none]'*) ;; *) changed=1; exit 0;; esac
 done
 current_frame=$(frame) || exit 0
 test "$current_frame" = "$painted" || { changed=1; exit 0; }
 case "$command" in
  F\ *)
   set -f
   set -- ${command#F }
   test "$#" = 9 || exit 0
   for value in "$@"; do
    case "$value" in ''|*[!0-9]*) exit 0;; esac
    test "$value" -le 160 || exit 0
   done;;
  *)
   case "$command" in ''|*[!0-9]*) exit 0;; esac
   test "$command" -le 100 || exit 0
   red=$((r * 160 * command / 255 / 100))
   green=$((g * 160 * command / 255 / 100))
   blue=$((b * 160 * command / 255 / 100))
   set -- "$red" "$green" "$blue" "$red" "$green" "$blue" "$red" "$green" "$blue";;
 esac
 requested=" $*"
 if test "$requested" != "$painted"; then
  for zone in 1 2 3; do
   for channel in red green blue; do
    printf '%s\n' "$1" > "$root/plum:$channel:ring$zone/brightness"
    shift
   done
  done
  painted=$requested
 fi
done
