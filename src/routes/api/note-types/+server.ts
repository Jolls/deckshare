import { json, error } from '@sveltejs/kit';
import { createNoteType, listNoteTypesForUser } from '$lib/server/db/queries/note-types';
import { requireUserId } from '../_util';
import type { RequestHandler } from './$types';

export const GET: RequestHandler = async ({ locals }) => {
	const userId = requireUserId(locals);
	return json(await listNoteTypesForUser(userId));
};

export const POST: RequestHandler = async ({ locals, request }) => {
	const userId = requireUserId(locals);
	const body = await request.json();
	if (
		typeof body?.name !== 'string' ||
		!Array.isArray(body?.fields) ||
		!Array.isArray(body?.templates)
	) {
		throw error(400, 'name, fields, and templates are required');
	}
	const noteType = await createNoteType(userId, {
		name: body.name,
		css: typeof body.css === 'string' ? body.css : undefined,
		isCloze: body.isCloze === true,
		fields: body.fields,
		templates: body.templates
	});
	return json(noteType, { status: 201 });
};
