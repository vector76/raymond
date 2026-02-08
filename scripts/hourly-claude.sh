#!/bin/bash

# Cron-like script that runs at 3 minutes past every hour
# Executes: claude -p "respond with just 'ok'"

last_run_hour=-1

while true; do
    # Get current time (%-H/%-M avoids leading zeros/octal issues)
    current_hour=$(date +%-H)
    current_min=$(date +%-M)
    current_sec=$(date +%-S)

    # Run if: it's a new hour we haven't run in AND minute >= 3
    if [ "$current_hour" -ne "$last_run_hour" ] && [ "$current_min" -ge 3 ]; then
        echo "$(date): Running claude command"
        claude -p "respond with just 'ok'" --model haiku
        last_run_hour=$current_hour
        sleep 5
    else
        # Calculate seconds until next X:03:00
        if [ "$current_min" -lt 3 ]; then
            # Before X:03 this hour, wait until X:03
            mins_to_wait=$((2 - current_min))
            secs_to_wait=$((60 - current_sec + mins_to_wait * 60))
        else
            # At or past X:03, wait until next hour's X:03
            mins_to_wait=$((62 - current_min))
            secs_to_wait=$((60 - current_sec + mins_to_wait * 60))
        fi
        echo "$(date): Sleeping for $secs_to_wait seconds until next X:03"
        sleep "$secs_to_wait"
    fi
done
