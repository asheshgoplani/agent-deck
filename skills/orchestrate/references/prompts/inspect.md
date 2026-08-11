{{include:delegated-task-preamble.md}}

# Bounded inspection

Perform this task read-only:

{{TASK}}

Write the complete result atomically to `{{ARTIFACT_PATH}}`, using a temporary
sibling followed by rename. Do not modify the repository, external systems, or
the inspected source. End with one concise deciding summary that tells the
conductor what action to route next and names the artifact path.
