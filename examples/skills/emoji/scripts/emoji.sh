#!/bin/sh

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

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
