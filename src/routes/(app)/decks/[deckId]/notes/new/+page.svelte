<script lang="ts">
	import { resolve } from '$app/paths';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const sortedFields = $derived(data.noteType.fields.slice().sort((a, b) => a.ordinal - b.ordinal));
</script>

<main>
	<h1>Add note to {data.deck.name}</h1>

	<form method="POST" action="?/create">
		{#each sortedFields as field (field.id)}
			<label>
				{field.name}
				<input type="text" name={field.name} />
			</label>
		{/each}

		<button type="submit">Add note</button>
		<a href={resolve(`/decks/${data.deck.id}`)}>Cancel</a>
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
</style>
