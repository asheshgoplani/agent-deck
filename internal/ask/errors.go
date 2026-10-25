package ask

import "errors"

// Refusal is an error the caller caused or may not override: the request is
// refused (CLI exit 2) with a fixed message. A Refusal message never carries
// a value, an answer or anything a vault printed.
type Refusal struct{ Msg string }

func (r *Refusal) Error() string { return r.Msg }

// IsRefusal reports whether err is (or wraps) a Refusal.
func IsRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}

// Refusals. Callers match them with errors.Is; details are wrapped with %w.
var (
	ErrDisabled        = &Refusal{"ask is off on this host ([ask] enabled = false)"}
	ErrName            = &Refusal{"bad name (want scope/NAME: scope [a-z0-9_-], NAME [A-Za-z0-9_.-] at most 64, no leading '.', no '..')"}
	ErrScope           = &Refusal{"name is not in an allowed scope ([ask] secret_scopes)"}
	ErrInvalid         = &Refusal{"invalid request"}
	ErrSandboxed       = &Refusal{"asks from sandboxed sessions are off ([ask] allow_sandboxed)"}
	ErrOneOpen         = &Refusal{"this session already has an open request"}
	ErrRateLimited     = &Refusal{"too many requests (5 per session and 20 per profile an hour)"}
	ErrExists          = &Refusal{"already exists in the vault; pass --replace to replace it"}
	ErrNotExists       = &Refusal{"does not exist in the vault; drop --replace"}
	ErrNotFound        = &Refusal{"no such request"}
	ErrNotPending      = &Refusal{"not pending"}
	ErrExpired         = &Refusal{"expired"}
	ErrHeaderMismatch  = &Refusal{"header does not match the request"}
	ErrNonceRequired   = &Refusal{"cancel needs the nonce the dialog announced (--nonce), or --unshown for a request no dialog showed"}
	ErrAlreadyShown    = &Refusal{"a dialog showed this request; cancel it with that dialog's nonce"}
	ErrReplaceMismatch = &Refusal{"the name's existence in the vault changed since the request; ask again"}
	ErrEmpty           = &Refusal{"empty value"}
	ErrTooLarge        = &Refusal{"value over 64 KiB"}
	ErrBadAnswer       = &Refusal{"answer not allowed"}
	ErrLooksLikeSecret = &Refusal{"looks like a secret; ask with `ask secret`"}
	ErrNoVault         = &Refusal{"no vault configured on this host"}
	ErrVaultUntrusted  = &Refusal{"vault " + vaultPathDefault + " (or a parent dir) is not root-owned or is writable by group or others"}
	ErrVaultRefused    = &Refusal{"vault refused"}
)

// ErrNoUI is returned by Ask with AwaitOptions.NoFallback when nothing marked
// the request seen within the fallback window (CLI exit 5).
var ErrNoUI = errors.New("nothing can show this request")
