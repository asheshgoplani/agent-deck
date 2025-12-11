# Agent Deck - Showcase Demo
# Slower paced demo showing: Empty start, Groups, Sessions, Status

Output demos/showcase.gif
Output demos/showcase.webm
Output demos/showcase.mp4

# Terminal settings
Set Shell "zsh"
Set Theme "TokyoNight"
Set FontSize 14
Set Width 1400
Set Height 850
Set TypingSpeed 60ms
Set Padding 15
Set WindowBar "Colorful"
Set BorderRadius 8
Set Framerate 30

# ============================================
# INTRO: Launch Agent Deck (SLOW - let user see)
# ============================================
Sleep 1s
Type "agent-deck -p default"
Sleep 1s
Enter
Sleep 4s

# ============================================
# PAUSE: Show empty dashboard
# ============================================
Sleep 3s

# ============================================
# CREATE GROUP: Press 'g' for new group
# ============================================
Type "g"
Sleep 1.5s

# Type group name
Type "My Project"
Sleep 800ms
Enter
Sleep 2.5s

# ============================================
# CREATE SESSION 1: Press 'n' for new session
# ============================================
Type "n"
Sleep 1.5s

# Session name
Type "Backend API"
Sleep 500ms
Tab
Sleep 400ms

# Project path
Type "~/code/backend"
Sleep 400ms
Tab
Sleep 400ms

# Select Claude (Down once)
Down
Sleep 300ms
Enter
Sleep 600ms

# Confirm creation
Enter
Sleep 3s

# ============================================
# CREATE SESSION 2: Another session
# ============================================
Type "n"
Sleep 1.5s

Type "Frontend"
Sleep 500ms
Tab
Sleep 400ms

Type "~/code/frontend"
Sleep 400ms
Tab
Sleep 400ms

# Select Gemini (Down twice)
Down
Down
Sleep 300ms
Enter
Sleep 600ms

Enter
Sleep 3s

# ============================================
# PAUSE: Show dashboard with group and sessions
# ============================================
Sleep 4s

# ============================================
# ATTACH: Enter to attach to session
# ============================================
Type "k"
Sleep 500ms
Enter
Sleep 3s

# Type a task in Claude
Type "Design the REST API authentication flow"
Sleep 800ms
Enter
Sleep 4s

# ============================================
# DETACH: Ctrl+Q to return to dashboard
# ============================================
Ctrl+Q
Sleep 3s

# ============================================
# SHOW: Status changed (green = active)
# ============================================
Sleep 3s

# ============================================
# SWITCH: Go to next session
# ============================================
Type "j"
Sleep 500ms
Enter
Sleep 3s

Type "Create React components for the login page"
Sleep 800ms
Enter
Sleep 4s

# Detach
Ctrl+Q
Sleep 3s

# ============================================
# FINAL: Show dashboard with statuses
# ============================================
Sleep 4s

# Quit
Type "q"
Sleep 1.5s
