#!/bin/sh

# Return an ASCII emoticon based on the input word
# Convert input to lowercase using tr for case-insensitive matching
word=$(echo "$1" | tr '[:upper:]' '[:lower:]')

case "$word" in
  "happy"|"smile"|"joy") echo ":-)" ;;
  "sad"|"cry") echo ":-(" ;;
  "shrug"|"dunno"|"whatever") echo "¯\_(ツ)_/¯" ;;
  "flip"|"angry"|"rage") echo "(╯°□°）╯︵ ┻━┻" ;;
  "table"|"putback") echo "┬─┬ノ( º _ ºノ)" ;;
  "magic"|"sparkle") echo "(ﾉ◕ヮ◕)ﾉ*:･ﾟ✧" ;;
  "sunglasses"|"cool") echo "(•_•) / ( •_•)>⌐■-■ / (⌐■_■)" ;;
  "sundar"|"ceo") echo "👓(⌐■_■) 👍" ;;
  "dance"|"party") echo "♪~ ᕕ(ᐛ)ᕗ" ;;
  "wink") echo ";-)" ;;
  "surprise"|"gasp") echo "(O_O)" ;;
  *) echo "¯\_(ツ)_/¯ (unknown word)" ;;
esac
