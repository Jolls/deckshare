import prettier from 'eslint-config-prettier';
import path from 'node:path';
import js from '@eslint/js';
import svelte from 'eslint-plugin-svelte';
import { defineConfig, includeIgnoreFile } from 'eslint/config';
import globals from 'globals';
import ts from 'typescript-eslint';

const gitignorePath = path.resolve(import.meta.dirname, '.gitignore');

export default defineConfig(
	includeIgnoreFile(gitignorePath),
	js.configs.recommended,
	ts.configs.recommended,
	svelte.configs.recommended,
	prettier,
	svelte.configs.prettier,
	{
		languageOptions: { globals: { ...globals.browser, ...globals.node } },
		rules: {
			// typescript-eslint strongly recommend that you do not use the no-undef lint rule on TypeScript projects.
			// see: https://typescript-eslint.io/troubleshooting/faqs/eslint/#i-get-errors-from-the-no-undef-rule-about-global-variables-not-being-defined-even-though-there-are-no-typescript-errors
			'no-undef': 'off'
		}
	},
	{
		files: ['**/*.svelte', '**/*.svelte.ts', '**/*.svelte.js'],
		languageOptions: {
			parserOptions: {
				projectService: true,
				extraFileExtensions: ['.svelte'],
				parser: ts.parser
			}
		}
	},
	{
		// src/lib/fsrs/ is isomorphic (CLAUDE.md §4, §17): pure functions, no DB, no
		// fetch, no browser globals. It is what guarantees client and server schedule
		// identically, so it is enforced here rather than left to convention.
		files: ['src/lib/fsrs/**/*.ts'],
		rules: {
			'no-restricted-globals': [
				'error',
				{ name: 'window', message: 'src/lib/fsrs/ must run unchanged on the server.' },
				{ name: 'document', message: 'src/lib/fsrs/ must run unchanged on the server.' },
				{ name: 'navigator', message: 'src/lib/fsrs/ must run unchanged on the server.' },
				{ name: 'location', message: 'src/lib/fsrs/ must run unchanged on the server.' },
				{ name: 'history', message: 'src/lib/fsrs/ must run unchanged on the server.' },
				{ name: 'localStorage', message: 'Persistence belongs outside src/lib/fsrs/.' },
				{ name: 'sessionStorage', message: 'Persistence belongs outside src/lib/fsrs/.' },
				{ name: 'indexedDB', message: 'Persistence belongs outside src/lib/fsrs/.' },
				{ name: 'caches', message: 'Persistence belongs outside src/lib/fsrs/.' },
				{ name: 'fetch', message: 'src/lib/fsrs/ must stay pure — no I/O (CLAUDE.md §2.6).' },
				{
					name: 'XMLHttpRequest',
					message: 'src/lib/fsrs/ must stay pure — no I/O (CLAUDE.md §2.6).'
				},
				{ name: 'WebSocket', message: 'src/lib/fsrs/ must stay pure — no I/O (CLAUDE.md §2.6).' },
				{ name: 'process', message: 'src/lib/fsrs/ must run unchanged in the browser.' },
				{ name: 'require', message: 'src/lib/fsrs/ must run unchanged in the browser.' },
				{ name: '__dirname', message: 'src/lib/fsrs/ must run unchanged in the browser.' },
				{ name: '__filename', message: 'src/lib/fsrs/ must run unchanged in the browser.' }
			],
			'no-restricted-imports': [
				'error',
				{
					patterns: [
						{
							group: ['$lib/server/*', '**/server/*'],
							message: 'src/lib/fsrs/ is not server-only.'
						},
						{
							group: ['$app/*', '$env/*'],
							message: 'SvelteKit runtime modules are not available on both sides of src/lib/fsrs/.'
						},
						{
							group: ['node:*', 'fs', 'path', 'crypto'],
							message: 'src/lib/fsrs/ must run unchanged in the browser.'
						},
						{
							group: ['drizzle-orm', 'drizzle-orm/*', 'postgres', 'better-sqlite3'],
							message: 'src/lib/fsrs/ never touches the database (CLAUDE.md §4).'
						}
					]
				}
			],
			'@typescript-eslint/no-explicit-any': 'error'
		}
	},
	{
		// Override or add rule settings here, such as:
		// 'svelte/button-has-type': 'error'
		rules: {}
	}
);
