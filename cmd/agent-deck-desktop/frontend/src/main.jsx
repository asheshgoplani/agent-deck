import React from 'react'
import {createRoot} from 'react-dom/client'
import './themes.css'
import './style.css'
import App from './App'
import { ThemeProvider } from './context/ThemeContext'

const container = document.getElementById('root')

const root = createRoot(container)

// Note: StrictMode removed to prevent double-mounting issues with xterm.js
root.render(
    <ThemeProvider>
        <App />
    </ThemeProvider>
)
