#!/bin/bash
# greenboot red script: a boot failed its checks. Record it for the Console
# (UpdateStatus.last_rollback); greenboot itself arranges the rollback.
date -u +"%Y-%m-%dT%H:%M:%SZ boot failed health checks; previous image restored" > /var/lib/maitre/last-rollback
