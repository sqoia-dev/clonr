import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// Charset omits visually ambiguous characters (0/O, 1/l) to reduce transcription errors.
// Modulo bias is acceptable for non-key material: with charset length 56 the bias is < 1.5%.
const TEMP_PASSWORD_CHARSET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"

/**
 * Generate a cryptographically random temporary password using the Web Crypto API.
 * Suitable for one-time-use admin-reset passwords that the user immediately changes.
 */
export function generateTempPassword(length = 24): string {
  const bytes = new Uint8Array(length)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => TEMP_PASSWORD_CHARSET[b % TEMP_PASSWORD_CHARSET.length]).join("")
}
