package http

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

// pathUUID parses the named path parameter as a UUID. ok is false when the parameter is
// missing or not a valid UUID -- the caller should treat that the same as "not found".
func pathUUID(r *http.Request, name string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	if err := u.Scan(r.PathValue(name)); err != nil {
		return pgtype.UUID{}, false
	}
	return u, true
}

func notFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}
