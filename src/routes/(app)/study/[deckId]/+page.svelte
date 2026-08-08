<script lang="ts">
	/**
	 * The reviewer (CLAUDE.md §6). The one rule this file exists to honour: grading is
	 * synchronous and local. `handleGrade` calls `$lib/fsrs`, advances the UI, and appends to
	 * the durable queue — there is no `await` on the network in it (invariant §2.6).
	 *
	 * Card HTML arrives already sanitised from the server (`getReviewSession` runs
	 * `sanitiseCardHtml`), which is what makes the `{@html}` below safe (CLAUDE.md §8).
	 */
	import { onMount } from 'svelte';
	import { Rating, type Grade } from '$lib/fsrs';
	import { currentCard, grade, startSession, WriteQueue, postReviewBatch } from '$lib/review';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// Deliberately a snapshot, not a derived: the session is fetched once at start (§6) and then
	// owned by the client. Reacting to `data` would discard grading progress on any invalidation.
	// svelte-ignore state_referenced_locally
	let session = $state(startSession(data.session));
	let showAnswer = $state(false);
	let pending = $state(0);
	let shownAt = Date.now();

	const card = $derived(currentCard(session));

	let queue: WriteQueue | undefined;

	onMount(() => {
		queue = new WriteQueue({ post: postReviewBatch, storage: localStorage });
		pending = queue.size;
		// Anything left over from a tab closed mid-session drains as soon as we're back.
		void queue.drain().then(() => (pending = queue?.size ?? 0));
	});

	function handleGrade(rating: Grade) {
		const result = grade(session, rating, new Date(), Date.now() - shownAt);
		if (!result) return;

		// 1. state applied, 2. UI advanced — both before anything touches the network.
		session = result.session;
		showAnswer = false;
		shownAt = Date.now();

		queue?.enqueue(result.event);
		pending = queue?.size ?? 0;
	}

	const KEYS: Record<string, Grade> = {
		'1': Rating.Again,
		'2': Rating.Hard,
		'3': Rating.Good,
		'4': Rating.Easy
	};

	function handleKey(event: KeyboardEvent) {
		if (event.key === ' ' || event.key === 'Enter') {
			event.preventDefault();
			if (!showAnswer) showAnswer = true;
			else handleGrade(Rating.Good);
			return;
		}
		const rating = KEYS[event.key];
		if (rating !== undefined && showAnswer) handleGrade(rating);
	}
</script>

<svelte:window onkeydown={handleKey} />

<main>
	{#if card}
		<article class="card" data-testid="card">
			<!-- `front`/`back` are the output of `sanitiseCardHtml` in `getReviewSession`; card
			     content is untrusted HTML and this is the one place it is allowed in (CLAUDE.md §8).
			     Do not point these at any value that has not been through that sanitiser. -->
			<!-- eslint-disable-next-line svelte/no-at-html-tags -->
			<div class="side">{@html card.front}</div>
			{#if showAnswer}
				<hr />
				<!-- eslint-disable-next-line svelte/no-at-html-tags -->
				<div class="side" data-testid="answer">{@html card.back}</div>
			{/if}
		</article>

		{#if showAnswer}
			<div class="grades">
				<button onclick={() => handleGrade(Rating.Again)}>Again (1)</button>
				<button onclick={() => handleGrade(Rating.Hard)}>Hard (2)</button>
				<button onclick={() => handleGrade(Rating.Good)}>Good (3)</button>
				<button onclick={() => handleGrade(Rating.Easy)}>Easy (4)</button>
			</div>
		{:else}
			<button data-testid="show-answer" onclick={() => (showAnswer = true)}>Show answer</button>
		{/if}
	{:else}
		<p data-testid="session-done">Nothing left to review today.</p>
	{/if}

	<footer>
		<span data-testid="remaining">{session.queue.length} left</span>
		<span data-testid="graded">{session.graded} graded</span>
		<span data-testid="pending">{pending} unsent</span>
	</footer>
</main>

<style>
	main {
		margin: 0 auto;
		max-width: 40rem;
		padding: 1rem;
	}
	.card {
		min-height: 8rem;
		padding: 1rem 0;
		text-align: center;
	}
	.grades {
		display: flex;
		gap: 0.5rem;
	}
	.grades button {
		flex: 1;
	}
	footer {
		display: flex;
		gap: 1rem;
		margin-top: 2rem;
		opacity: 0.7;
	}
</style>
