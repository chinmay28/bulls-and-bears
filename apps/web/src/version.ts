/**
 * The running client's version, `vYEAR.MONTH.PATCH`, where the leading numbers
 * are the release line's year and month and the patch number is the
 * repository's commit count (so `v2026.8.42` is the 42nd commit on the 2026.8
 * line).
 *
 * Inlined at build time by Vite's `define` (see vite.config.ts) from
 * scripts/version.mjs — the same source the binary is stamped from, so the
 * header and Settings always agree. Patch `0` means a build made without git
 * available.
 */
declare const __APP_VERSION__: string

export const APP_VERSION: string = __APP_VERSION__
