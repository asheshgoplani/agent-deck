# Original main baseline: PARTIAL

Production revision: `7d2302fb8a41c5fcab7a441d56729b4b6547885a` (v1.16.10), with benchmark-only driver/UI overlays. Platform: `linux-arm64`. Machine: `docker-arm64-16cpu-shared`. Seed: 2286. Runs: 3. Corrected mixed Claude/Codex/shell fixture.

The 10- and 100-session workers completed and their raw samples were recovered from per-size progress files. The 500-session `list --json` invocation hit its 120-second timeout. That failure has no latency percentile; no 500-session rows are invented or replaced with zero. RSS and open fds were unavailable in the pre-health harness.

This is historical partial evidence, not the complete baseline for the regression gate. Use [linux-arm64.json](linux-arm64.json) for the complete health-stack measurement set. The partial JSON preserves the original samples, explicit missing coverage and timeout outcome. Default three-run p95 is the largest observation; key-repeat rows contain 300 frames.

| Sessions | Metric | Unit | Samples | p50 | p95 |
|---:|---|---|---:|---:|---:|
| 10 | cold_start_populated_terminal_frame_ms | ms | 3 | 5259.758002 | 5400.606503 |
| 10 | cold_start_populated_terminal_frame_tmux_subprocesses | count | 3 | 5.000000 | 5.000000 |
| 10 | goroutines | count | 3 | 4.000000 | 4.000000 |
| 10 | group_activity_map_ms | ms | 3 | 0.002731 | 0.021927 |
| 10 | list_json_ms | ms | 3 | 230.491000 | 353.048000 |
| 10 | list_json_tmux_subprocesses | count | 3 | 48.000000 | 48.000000 |
| 10 | orphan_hook_files_remaining | count | 3 | 30.000000 | 30.000000 |
| 10 | remote_auth_refresh_ms | ms | 3 | 0.770000 | 4.148000 |
| 10 | remote_auth_refresh_ssh_subprocesses | count | 3 | 2.000000 | 2.000000 |
| 10 | remote_fast_refresh_ms | ms | 3 | 0.680000 | 0.836000 |
| 10 | remote_fast_refresh_ssh_subprocesses | count | 3 | 1.000000 | 1.000000 |
| 10 | remote_slow_refresh_ms | ms | 3 | 6003.186000 | 6010.243000 |
| 10 | remote_slow_refresh_ssh_subprocesses | count | 3 | 1.000000 | 3.000000 |
| 10 | send_cli_unconfirmed | count | 3 | 1.000000 | 1.000000 |
| 10 | send_round_trip_ms | ms | 3 | 7260.086000 | 7589.152000 |
| 10 | send_round_trip_tmux_subprocesses | count | 3 | 98.000000 | 98.000000 |
| 10 | session_show_ms | ms | 3 | 116.679000 | 137.102000 |
| 10 | session_show_tmux_subprocesses | count | 3 | 3.000000 | 3.000000 |
| 10 | status_pass_ms | ms | 3 | 130.753000 | 147.510500 |
| 10 | tmux_calls | count | 3 | 60.000000 | 62.000000 |
| 10 | tui_first_populated_frame_ms | ms | 3 | 34.062208 | 38.852208 |
| 10 | tui_key_repeat_frame_ms | ms | 300 | 0.742041 | 2.143000 |
| 100 | cold_start_populated_terminal_frame_ms | ms | 3 | 5218.928669 | 5932.968920 |
| 100 | cold_start_populated_terminal_frame_tmux_subprocesses | count | 3 | 5.000000 | 5.000000 |
| 100 | goroutines | count | 3 | 4.000000 | 4.000000 |
| 100 | group_activity_map_ms | ms | 3 | 0.028964 | 0.037904 |
| 100 | list_json_ms | ms | 3 | 23600.134000 | 27110.836000 |
| 100 | list_json_tmux_subprocesses | count | 3 | 4020.000000 | 4096.000000 |
| 100 | orphan_hook_files_remaining | count | 3 | 300.000000 | 300.000000 |
| 100 | remote_auth_refresh_ms | ms | 3 | 0.735000 | 2.307000 |
| 100 | remote_auth_refresh_ssh_subprocesses | count | 3 | 1.000000 | 1.000000 |
| 100 | remote_fast_refresh_ms | ms | 3 | 1.704000 | 8.547000 |
| 100 | remote_fast_refresh_ssh_subprocesses | count | 3 | 1.000000 | 2.000000 |
| 100 | remote_slow_refresh_ms | ms | 3 | 6006.638000 | 6037.825000 |
| 100 | remote_slow_refresh_ssh_subprocesses | count | 3 | 1.000000 | 2.000000 |
| 100 | send_cli_unconfirmed | count | 3 | 1.000000 | 1.000000 |
| 100 | send_round_trip_ms | ms | 3 | 7036.471000 | 8316.436000 |
| 100 | send_round_trip_tmux_subprocesses | count | 3 | 98.000000 | 98.000000 |
| 100 | session_show_ms | ms | 3 | 88.413000 | 115.345000 |
| 100 | session_show_tmux_subprocesses | count | 3 | 3.000000 | 3.000000 |
| 100 | status_pass_ms | ms | 3 | 1814.003250 | 3101.156168 |
| 100 | tmux_calls | count | 3 | 3485.000000 | 3526.000000 |
| 100 | tui_first_populated_frame_ms | ms | 3 | 33.584834 | 53.857333 |
| 100 | tui_key_repeat_frame_ms | ms | 300 | 0.757500 | 1.624375 |
