-- Queries for the public catalog. sqlc turns each "-- name: X :kind" block
-- into a Go method X on catalogdb.Queries:
--   :one  → returns a single row (pgx.ErrNoRows if none)
--   :many → returns a slice
--
-- Every list ends its ORDER BY with "id" so that ties are broken the same way
-- every time; without it PostgreSQL may return equal rows in any order and
-- pagination could skip or repeat rows.
--
-- Public queries over materials filter "hidden_at IS NULL" (spec R-API-20).

-- name: SearchCourses :many
-- sqlc.arg(query) must already be LIKE-escaped by the caller (\ % _ prefixed
-- with a backslash), so user input is matched literally. '' matches all.
SELECT * FROM courses
WHERE sqlc.arg(query)::text = ''
   OR code  ILIKE '%' || sqlc.arg(query)::text || '%'
   OR title ILIKE '%' || sqlc.arg(query)::text || '%'
ORDER BY code, id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountCourses :one
SELECT count(*) FROM courses
WHERE sqlc.arg(query)::text = ''
   OR code  ILIKE '%' || sqlc.arg(query)::text || '%'
   OR title ILIKE '%' || sqlc.arg(query)::text || '%';

-- name: GetCourseBySlug :one
SELECT * FROM courses WHERE slug = $1;

-- name: ListOfferingsByCourse :many
-- Newest first; within a year fall → summer → spring (R-EP-8).
SELECT * FROM offerings
WHERE course_id = $1
ORDER BY year DESC,
         CASE term WHEN 'fall' THEN 1 WHEN 'summer' THEN 2 ELSE 3 END,
         instructor NULLS LAST,
         id;

-- name: ListAssessmentsByCourse :many
-- All assessments of all offerings of a course in one query (instead of one
-- query per offering, the classic "N+1" problem). Go groups them afterwards.
SELECT * FROM assessments
WHERE offering_id IN (SELECT o.id FROM offerings o WHERE o.course_id = $1)
ORDER BY offering_id,
         CASE kind WHEN 'quiz' THEN 1 WHEN 'midterm' THEN 2 WHEN 'final' THEN 3
                   WHEN 'assignment' THEN 4 WHEN 'lab' THEN 5 ELSE 6 END,
         number NULLS LAST,
         label NULLS LAST,
         id;

-- name: GetOffering :one
SELECT * FROM offerings WHERE id = $1;

-- name: ListVisibleMaterialsByOffering :many
-- General materials (no assessment) first, then in assessment order (R-EP-14).
-- The LEFT JOIN is only used for sorting; we still return material columns.
SELECT m.id, m.offering_id, m.assessment_id, m.title, m.type, m.description, m.published_at, m.hidden_at
FROM materials m
LEFT JOIN assessments a ON a.id = m.assessment_id
WHERE m.offering_id = $1 AND m.hidden_at IS NULL
ORDER BY (m.assessment_id IS NOT NULL),
         CASE a.kind WHEN 'quiz' THEN 1 WHEN 'midterm' THEN 2 WHEN 'final' THEN 3
                     WHEN 'assignment' THEN 4 WHEN 'lab' THEN 5 ELSE 6 END,
         a.number NULLS LAST,
         a.label NULLS LAST,
         a.id,
         m.type,
         m.title,
         m.id;

-- name: GetVisibleMaterial :one
SELECT * FROM materials WHERE id = $1 AND hidden_at IS NULL;

-- name: GetMaterialContext :one
-- The course / offering / assessment a material belongs to, so the material
-- page can show "CSCI 151 · 2025 Fall · Midterm 1" (R-EP-15). Assessment
-- columns come from a LEFT JOIN and are therefore nullable.
SELECT c.id    AS course_id,
       c.code  AS course_code,
       c.slug  AS course_slug,
       c.title AS course_title,
       o.id    AS offering_id,
       o.year  AS offering_year,
       o.term  AS offering_term,
       o.instructor AS offering_instructor,
       a.id     AS assessment_id,
       a.kind   AS assessment_kind,
       a.number AS assessment_number,
       a.label  AS assessment_label
FROM materials m
JOIN offerings o ON o.id = m.offering_id
JOIN courses c   ON c.id = o.course_id
LEFT JOIN assessments a ON a.id = m.assessment_id
WHERE m.id = $1;

-- name: ListMaterialFiles :many
SELECT * FROM material_files WHERE material_id = $1 ORDER BY position;

-- name: GetVisibleMaterialFile :one
-- Joins up to the material (for visibility) and the course (for a readable
-- download filename). A file of a hidden material is simply "not found".
SELECT f.id,
       f.storage_key,
       f.mime_type,
       f.sha256,
       f.position,
       m.title AS material_title,
       c.slug  AS course_slug
FROM material_files f
JOIN materials m ON m.id = f.material_id
JOIN offerings o ON o.id = m.offering_id
JOIN courses c   ON c.id = o.course_id
WHERE f.id = $1 AND m.hidden_at IS NULL;

-- The Create* queries are used by the seed command and tests now, and by the
-- moderation "approve" flow in Plan 5.

-- name: CreateCourse :one
INSERT INTO courses (code, slug, title, department)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreateOffering :one
INSERT INTO offerings (course_id, year, term, instructor)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreateAssessment :one
INSERT INTO assessments (offering_id, kind, number, label)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreateMaterial :one
INSERT INTO materials (offering_id, assessment_id, title, type, description)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreateMaterialFile :one
-- The id is supplied by the caller because it is part of storage_key (R-DB-15).
INSERT INTO material_files (id, material_id, storage_key, mime_type, size_bytes, sha256, position)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;
