-- name: CreateUser :one
INSERT INTO users (name, is_admin) VALUES ($1, $2)
RETURNING id, name, is_admin, created_at;
