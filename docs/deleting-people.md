# Deleting a student or a parent

Removing a person is one operation, not fifteen. These two endpoints delete a
student or a parent together with everything that references them, inside a
single transaction.

Cross-repo context lives in the vault (`../jtrax-docs/features/`). This file
covers the endpoints and the order they enforce.

## Why the generic DELETE is not enough

`DELETE /api/v1/<resource>/{id}` refuses a row anything else points at, which is
right for a class or a credit package — you should not silently lose a term of
attendance because someone tidied a dropdown. It is wrong for a person: a child
who has ever attended a class has attendance, credit transactions, payments, an
enrolment and a link to a guardian, so the console had to delete five kinds of
row in the right order before the student themselves would go. Getting the order
wrong left the record half-removed, and there was no transaction to undo it.

## Endpoints

Both are staff-only (`Admin`, `Receptionist`), like every other delete.

```
DELETE /api/v1/students/{id}/cascade[?parent=orphan]
DELETE /api/v1/parents/{id}/cascade[?children=delete]
```

`parent=orphan` also removes the guardian **when this was their only child** — a
parent login with nobody attached can see nothing. A guardian with other
children is always kept.

`children=delete` cascades each linked child before removing the parent.
Without it the children stay and simply lose their guardian, which is the usual
case: one parent leaves, another is linked later.

Both answer with what actually went:

```json
{
  "status": "deleted",
  "student": "stu_penny",
  "parent": "par_sandy",
  "children": ["stu_uri"],
  "accounts_kept": 0,
  "attendance_rows": 12,
  "payment_rows": 3
}
```

## Order

Forced by the foreign keys. `credit_transaction` references the enrolment, the
payment *and* the attendance row, so it goes first of all:

1. `credit_transaction` — by enrolment, by payment, by attendance
2. `attendance`, `payment`
3. `student_enrollment`
4. `practice_activity`, `practice_settings`, `puzzle_attempt`,
   `tournament_registration`
5. `student_parent`
6. `student`
7. `auth_session`, `password_reset`, then `user_account`

A parent is `parent_contact` → `notification_preference` → `student_parent` →
`parent` → the account.

## Accounts that stay behind

The login is deleted last, and only if nothing outside the ER model still
references it — a game the student played (`game_room.white_account_id`), an
announcement they posted. Rewriting that history to free the row would be a
worse trade than one orphaned login, so the account is kept and
`accounts_kept` counts it. SQLite leaves a transaction usable after a
constraint failure, so a refusal there costs nothing else in the cascade.

## Tests

`internal/api/people_test.go` drives the seeded family — Sandy with Penny and
Uri, Penny carrying attendance, credits, a payment and an enrolment — through
every branch: the plain delete still refusing, the cascade clearing each
referencing table, a guardian kept when a sibling remains, taken when none does,
children kept by default and removed on request, and all four roles checked
against the route.
