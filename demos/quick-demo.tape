# Agent Deck - Quick Demo
# Shows: Empty start, Create session, Run Claude, Search task

Output demos/quick-demo.gif
Output demos/quick-demo.mp4

Set Shell "zsh"
Set Theme "TokyoNight"
Set FontSize 14
Set Width 1400
Set Height 850
Set TypingSpeed 50ms
Set Padding 15
Set WindowBar "Colorful"
Set BorderRadius 8
Set Framerate 30

# ============================================
# Launch Agent Deck (empty state)
# ============================================
Sleep 1s
Type "agent-deck -p default"
Sleep 800ms
Enter
Sleep 3s

# ============================================
# Create new session with 'n'
# ============================================
Type "n"
Sleep 1.5s

# Session name
Type "Code Search"
Sleep 400ms
Tab
Sleep 300ms

# Project path
Type "~/projects/myapp"
Sleep 400ms
Tab
Sleep 300ms

# Select Claude (Down once)
Down
Sleep 200ms
Enter
Sleep 500ms

# Confirm
Enter
Sleep 3s

# ============================================
# Attach to session (Enter)
# ============================================
Enter
Sleep 3s

# ============================================
# Type a search task in Claude
# ============================================
Type "Find all API endpoints in the codebase"
Sleep 600ms
Enter
Sleep 4s

# ============================================
# Detach with Ctrl+Q
# ============================================
Ctrl+Q
Sleep 3s

# ============================================
# Show final state
# ============================================
Sleep 2s

Type "q"
Sleep 1s
