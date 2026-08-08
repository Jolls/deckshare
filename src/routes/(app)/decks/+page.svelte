<script lang="ts">
	import { resolve } from '$app/paths';
	import type { ActionData, PageData } from './$types';

	let { data, form }: { data: PageData; form: ActionData } = $props();
</script>

<main>
	<h1>Decks</h1>

	{#if data.decks.length === 0}
		<p>No decks yet.</p>
	{:else}
		<ul>
			{#each data.decks as { deck } (deck.id)}
				<li><a href={resolve(`/decks/${deck.id}`)}>{deck.name}</a></li>
			{/each}
		</ul>
	{/if}

	<h2>Create a deck</h2>
	<form method="POST" action="?/create">
		{#if form?.error}
			<p role="alert" class="error">{form.error}</p>
		{/if}

		<label>
			Name
			<input type="text" name="name" required />
		</label>

		<label>
			Description
			<input type="text" name="description" />
		</label>

		<button type="submit">Create deck</button>
	</form>
</main>

<style>
	main {
		margin: 0 auto;
		max-width: 32rem;
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
