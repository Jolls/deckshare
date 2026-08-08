<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import {
		validateDisplayName,
		validateEmailFormat,
		validatePasswordLength
	} from '$lib/auth/validation';

	const FALLBACK_ERROR = 'Something went wrong. Try again.';

	let email = $state('');
	let password = $state('');
	let displayName = $state('');
	let error = $state<string | null>(null);
	let submitting = $state(false);

	async function handleSubmit(event: SubmitEvent) {
		event.preventDefault();

		error =
			validateEmailFormat(email) ??
			validatePasswordLength(password) ??
			validateDisplayName(displayName);
		if (error) return;

		submitting = true;
		try {
			const res = await fetch('/signup', {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: JSON.stringify({ email, password, displayName })
			});
			if (res.ok) {
				await goto(resolve('/decks'));
				return;
			}
			const body = await res.json().catch(() => null);
			error = body?.error ?? FALLBACK_ERROR;
		} catch {
			error = FALLBACK_ERROR;
		} finally {
			submitting = false;
		}
	}
</script>

<main>
	<h1>Sign up</h1>

	<form onsubmit={handleSubmit}>
		{#if error}
			<p role="alert" class="error">{error}</p>
		{/if}

		<label>
			Email
			<input type="email" bind:value={email} autocomplete="email" required />
		</label>

		<label>
			Password
			<input type="password" bind:value={password} autocomplete="new-password" required />
		</label>

		<label>
			Display name
			<input type="text" bind:value={displayName} autocomplete="name" required />
		</label>

		<button type="submit" disabled={submitting}>Sign up</button>
	</form>

	<p><a href={resolve('/login')}>Already have an account? Log in</a></p>
</main>

<style>
	main {
		margin: 0 auto;
		max-width: 24rem;
		padding: 1rem;
	}
	form {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}
	label {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}
	.error {
		color: #b91c1c;
	}
</style>
