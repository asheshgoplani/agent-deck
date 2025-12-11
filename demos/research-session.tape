# Demo: Create a Research Session in Agent Deck
# Shows: Launch → Create Session → Attach → Research → Detach

Output demos/research-session.gif

# Terminal settings
Set Theme "TokyoNight"
Set FontSize 14
Set Width 1400
Set Height 800
Set TypingSpeed 40ms
Set Padding 15
Set WindowBar "Colorful"
Set BorderRadius 8

# Launch agent-deck
Type "agent-deck"
Sleep 300ms
Enter
Sleep 2s

# Press 'n' to create new session
Type "n"
Sleep 800ms

# Type session name
Type "Research Session"
Sleep 300ms

# Tab to project path
Tab
Sleep 200ms
Type "/Users/ashesh"
Sleep 300ms

# Tab to command selector, select claude
Tab
Sleep 200ms
Down
Sleep 200ms
Enter
Sleep 500ms

# Confirm creation (Enter on Create button)
Enter
Sleep 2s

# Navigate to the new session and attach
Type "j"
Sleep 300ms
Enter
Sleep 4s

# Type a research task in Claude
Type "Research the best practices for Go error handling in 2025"
Sleep 500ms
Enter
Sleep 3s

# Detach with Ctrl+Q
Ctrl+Q
Sleep 2s

# Show the session is now in the list with status
Sleep 2s

# Quit
Type "q"
Sleep 500ms
