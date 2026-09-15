# Lessons

- Delayed subprocess regression fixtures must register gate-release cleanup before waiting for entry and explicitly join every worker after a timeout. Acquiring the worker's mutex does not prove that worker has run.
