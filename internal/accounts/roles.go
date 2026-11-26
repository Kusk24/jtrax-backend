// Access checks shared by school workflows.
package accounts

func IsStaff(role string) bool { return role == "Admin" || role == "Receptionist" }
