# Agent Deck - Full Feature Demo
# Shows: Multi-session, Context Switching, Status, Rename

Output demos/full-demo.gif
Output demos/full-demo.webm

# Terminal settings
Set Shell "zsh"
Set Theme "TokyoNight"
Set FontSize 14
Set Width 1400
Set Height 850
Set TypingSpeed 35ms
Set Padding 15
Set WindowBar "Colorful"
Set BorderRadius 8
Set Framerate 30

# ============================================
# INTRO: Launch Agent Deck
# ============================================
Sleep 500ms
Type "agent-deck"
Sleep 300ms
Enter
Sleep 2s

# ============================================
# CREATE SESSION 1: Claude - API Research
# ============================================
Type "n"
Sleep 600ms
Type "API Research"
Tab
Sleep 200ms
Type "~/projects/api"
Tab
Sleep 200ms
Down
Sleep 150ms
Enter
Sleep 400ms
Enter
Sleep 1500ms

# ============================================
# CREATE SESSION 2: Gemini - Database Design
# ============================================
Type "n"
Sleep 600ms
Type "Database Design"
Tab
Sleep 200ms
Type "~/projects/db"
Tab
Sleep 200ms
Down
Down
Sleep 150ms
Enter
Sleep 400ms
Enter
Sleep 1500ms

# ============================================
# CREATE SESSION 3: Aider - Code Review
# ============================================
Type "n"
Sleep 600ms
Type "Code Review"
Tab
Sleep 200ms
Type "~/projects/review"
Tab
Sleep 200ms
Down
Down
Down
Sleep 150ms
Enter
Sleep 400ms
Enter
Sleep 2s

# ============================================
# ATTACH TO CLAUDE SESSION
# ============================================
Type "k"
Sleep 200ms
Type "k"
Sleep 300ms
Enter
Sleep 3s

# Type research task in Claude
Type "Research best practices for REST API authentication in 2025"
Sleep 400ms
Enter
Sleep 4s

# ============================================
# CONTEXT SWITCH: Detach with Ctrl+Q
# ============================================
Ctrl+Q
Sleep 1500ms

# ============================================
# SWITCH TO GEMINI SESSION
# ============================================
Type "j"
Sleep 300ms
Enter
Sleep 3s

# Type task in Gemini
Type "Design a schema for user analytics with time-series data"
Sleep 400ms
Enter
Sleep 4s

# ============================================
# CONTEXT SWITCH AGAIN
# ============================================
Ctrl+Q
Sleep 1500ms

# ============================================
# SHOW STATUS: Sessions have different states
# ============================================
Sleep 2s

# ============================================
# RENAME A SESSION
# ============================================
Type "k"
Sleep 200ms
Type "G"
Sleep 600ms
Type "Claude API Research"
Sleep 300ms
Enter
Sleep 1500ms

# ============================================
# FINAL: Show all sessions with statuses
# ============================================
Sleep 2s
Type "q"
Sleep 500ms
