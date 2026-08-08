<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();

	async function handleLogout() {
		await fetch('/logout', { method: 'POST' });
		await goto(resolve('/login'));
	}
</script>

<header>
	<button onclick={handleLogout}>Log out</button>
</header>

{@render children()}

<style>
	header {
		display: flex;
		justify-content: flex-end;
		padding: 0.5rem 1rem;
	}
</style>
