#!/bin/sh
# Fake curl for the example azure-vm provider tests: answers the public-IP
# lookup with $FAKE_PUBLIC_IP and records the URL to $FAKE_AZ_STATE/curl.log.
s="${FAKE_AZ_STATE:?FAKE_AZ_STATE not set}"
printf '%s\n' "$*" >>"$s/curl.log"
printf '%s\n' "${FAKE_PUBLIC_IP:?FAKE_PUBLIC_IP not set}"
