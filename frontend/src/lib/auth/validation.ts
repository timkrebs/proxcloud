// Pure, side-effect-free auth input validation. These mirror the server's
// rules (email shape, password >= 12, confirm match) so the UI can give
// immediate feedback — but the server stays authoritative: every submit still
// surfaces the backend's ApiError on failure.

/** Minimum password length enforced by POST /api/auth/bootstrap and /password. */
export const MIN_PASSWORD_LEN = 12;

/** The single copy for the password-length rule (hint + inline error). */
export const PASSWORD_RULE = `At least ${MIN_PASSWORD_LEN} characters`;

/** Basic email shape: non-empty local@domain.tld with no whitespace. */
export function isValidEmail(email: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());
}

/** Client-side password check mirroring the server's >= 12 rule. */
export function isValidPassword(password: string): boolean {
  return password.length >= MIN_PASSWORD_LEN;
}
