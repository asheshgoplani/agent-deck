// auth.js -- In-memory holder for the bearer token.
//
// Lives in its own module so api.js can read the token without importing
// state.js. state.js imports api.js (for archived-session loading), and
// api.js importing state.js back closed an import cycle
// (api.js -> state.js -> api.js). state.js re-exports this signal, so every
// existing consumer that takes authTokenSignal from state.js keeps working.
//
// main.js reads the token from the URL, stores it here, and strips it from
// the address bar. It must never be written to Web Storage or a cookie; see
// TestAuthTokenIsNeverPersistedToWebStorage.
import { signal } from '@preact/signals'

export const authTokenSignal = signal('')
