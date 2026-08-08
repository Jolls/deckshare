<script lang="ts">
	import { resolve } from '$app/paths';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const sortedFields = $derived(data.noteType.fields.slice().sort((a, b) => a.ordinal - b.ordinal));
</script>

<main>
	<h1>{data.deck.name}</h1>
	{#if data.deck.description}<p>{data.deck.description}</p>{/if}

	<p>
		<a href={resolve(`/decks/${data.deck.id}/notes/new`)}>Add note</a>
	</p>

	{#if data.notes.length === 0}
		<p>No notes yet.</p>
	{:else}
		<table>
			<thead>
				<tr>
					{#each sortedFields as field (field.id)}
						<th>{field.name}</th>
					{/each}
					<th></th>
				</tr>
			</thead>
			<tbody>
				{#each data.notes as note (note.id)}
					<tr>
						{#each note.fields as value, i (i)}
							<td>{value}</td>
						{/each}
						<td>
							<form method="POST" action="?/deleteNote">
								<input type="hidden" name="noteId" value={note.id} />
								<button type="submit">Delete</button>
							</form>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</main>

<style>
	main {
		margin: 0 auto;
		max-width: 48rem;
		padding: 1rem;
	}
	table {
		border-collapse: collapse;
		width: 100%;
	}
	th,
	td {
		text-align: left;
		padding: 0.25rem 0.5rem;
		border-bottom: 1px solid #ddd;
	}
</style>
