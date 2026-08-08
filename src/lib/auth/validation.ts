/**
 * Client-side mirror of the server's validation rules — NOT the source of truth, the
 * server (src/lib/server/auth/signup.ts, login.ts) is. This file exists only to avoid a
 * round-trip on obvious errors; keep it in sync by hand if those files change.
 */

export const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
export const EMAIL_MAX_LENGTH = 255;
export const PASSWORD_MIN_LENGTH = 8;
export const PASSWORD_MAX_LENGTH = 200;
export const DISPLAY_NAME_MAX_LENGTH = 255;

/** Mirrors signup.ts's email check. */
export function validateEmailFormat(email: string): string | null {
	if (!EMAIL_RE.test(email) || email.length > EMAIL_MAX_LENGTH) {
		return 'Invalid email address';
	}
	return null;
}

/** For login: server doesn't validate format, only rejects oversized input. */
export function validateEmailMaxLength(email: string): string | null {
	if (email.length > EMAIL_MAX_LENGTH) return 'Invalid email or password';
	return null;
}

/** Mirrors signup.ts's password length check. */
export function validatePasswordLength(password: string): string | null {
	if (password.length < PASSWORD_MIN_LENGTH || password.length > PASSWORD_MAX_LENGTH) {
		return `Password must be between ${PASSWORD_MIN_LENGTH} and ${PASSWORD_MAX_LENGTH} characters`;
	}
	return null;
}

/** For login: server only rejects oversized passwords, not short ones (existing accounts). */
export function validatePasswordMaxLength(password: string): string | null {
	if (password.length > PASSWORD_MAX_LENGTH) return 'Invalid email or password';
	return null;
}

/** Mirrors signup.ts's display-name check. */
export function validateDisplayName(displayName: string): string | null {
	if (!displayName.trim() || displayName.length > DISPLAY_NAME_MAX_LENGTH) {
		return 'Display name is required';
	}
	return null;
}
