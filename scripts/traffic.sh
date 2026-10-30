#!/usr/bin/env bash
# Sends irregular traffic to the server so metrics and traces have something to show.
# Switches between quiet stretches, steady load, parallel bursts and full silence in
# random order, with a mix of normal requests, 404s, 405s and malformed bodies.
#
# usage: scripts/traffic.sh [base-url]     default http://localhost (the kind ingress)
#        DURATION=120 scripts/traffic.sh   stop after 120s, default runs until ctrl+c
#

set -uo pipefail

BASE_URL="${1:-http://localhost}"
DURATION="${DURATION:-0}"
NAMES=(Alice Bob Carol Dave Erin Frank Grace Heidi Ivan Judy)

deadline=0
if (( DURATION > 0 )); then
  deadline=$((SECONDS + DURATION))
fi

# every request appends its status code here, report() sums them up per phase
tally=$(mktemp)
trap 'kill $(jobs -p) 2>/dev/null; rm -f "$tally"; echo; exit 0' INT TERM

rand() {
  echo $(( $1 + RANDOM % ($2 - $1 + 1) ))
}

pause() {
  local ms
  ms=$(rand "$1" "$2")
  sleep "$((ms / 1000)).$(printf '%03d' $((ms % 1000)))"
}

call() {
  local args=(-s -o /dev/null -w '%{http_code}' --max-time 5 -X "$1")
  if (( $# >= 3 )); then
    args+=(-H 'Content-Type: application/json' -d "$3")
  fi
  local code
  code=$(curl "${args[@]}" "$BASE_URL$2" 2>/dev/null)
  echo "${code:-000}" >>"$tally"
}

one_request() {
  local roll name
  roll=$(rand 1 100)
  name=${NAMES[RANDOM % ${#NAMES[@]}]}

  if   (( roll <= 35 )); then call GET /users
  elif (( roll <= 50 )); then call POST /users "{\"name\":\"$name\"}"
  elif (( roll <= 60 )); then call PUT "/users/$(rand 1 12)" "{\"name\":\"$name\"}" # ids that don't exist yet give 404
  elif (( roll <= 70 )); then call GET /hello
  elif (( roll <= 75 )); then call GET /health
  elif (( roll <= 85 )); then call GET "/nope/$RANDOM"                              # 404
  elif (( roll <= 92 )); then call DELETE /users                                   # 405
  else                         call POST /users '{"name":'                         # malformed JSON, 400
  fi
}

# phase_end SECS prints when a phase of that length ends, never past the overall deadline
phase_end() {
  local end=$((SECONDS + $1))
  if (( deadline > 0 && deadline < end )); then
    end=$deadline
  fi
  echo "$end"
}

# one request every 1-3s
quiet() {
  label="quiet"
  local end
  end=$(phase_end "$(rand 10 30)")
  while (( SECONDS < end )); do
    one_request
    pause 800 3000
  done
}

# a few requests per second
steady() {
  label="steady"
  local end
  end=$(phase_end "$(rand 20 60)")
  while (( SECONDS < end )); do
    one_request
    pause 50 400
  done
}

# several clients at once, each around 15-30 requests per second
burst() {
  local end workers
  end=$(phase_end "$(rand 5 15)")
  workers=$(rand 4 12)
  label="burst x$workers"
  for _ in $(seq "$workers"); do
    ( while (( SECONDS < end )); do one_request; pause 10 60; done ) &
  done
  wait
}

# no requests at all, shows up as a gap in the graphs
silence() {
  label="silence"
  local end
  end=$(phase_end "$(rand 5 20)")
  while (( SECONDS < end )); do
    sleep 1
  done
}

# report SECS prints one line per phase, e.g. "14:02:11  burst x7   12s  200x310 201x88 400x41 ..."
report() {
  local counts
  counts=$(sort "$tally" | uniq -c | awk '{printf "%sx%s ", $2, $1}')
  printf '%s  %-10s %3ss  %s\n' "$(date +%T)" "$label" "$1" "${counts:-no requests}"
  : >"$tally"
}

if ! curl -s -o /dev/null --max-time 3 "$BASE_URL/health"; then
  echo "can't reach $BASE_URL/health, is the server up? (make deploy-local)" >&2
  rm -f "$tally"
  exit 1
fi
echo "sending irregular traffic to $BASE_URL, ctrl+c to stop"

while (( deadline == 0 || SECONDS < deadline )); do
  start=$SECONDS
  roll=$(rand 1 100)
  if   (( roll <= 30 )); then quiet
  elif (( roll <= 65 )); then steady
  elif (( roll <= 85 )); then burst
  else                        silence
  fi
  report $((SECONDS - start))
done

rm -f "$tally"