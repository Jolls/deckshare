import { sql } from 'drizzle-orm';
import { db } from '$lib/server/db';
import { users } from '$lib/server/db/schema';

/** Case-insensitive lookup, matching the `lower(email)` unique index. */
export async function getUserByEmail(email: string) {
	const [user] = await db
		.select()
		.from(users)
		.where(sql`lower(${users.email}) = lower(${email})`);
	return user;
}

export async function createUser(params: {
	email: string;
	passwordHash: string;
	displayName: string;
}) {
	const [user] = await db.insert(users).values(params).returning();
	if (!user) throw new Error('insert into users returned no row');
	return user;
}
