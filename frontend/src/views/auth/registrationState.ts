// One-use handoff between registration and email verification. Never persist this state.
let pendingPassword: { email: string; password: string } | null = null

export function rememberRegistrationPassword(email: string, password: string): void {
  pendingPassword = { email, password }
}

export function takeRegistrationPassword(email: string): string {
  const pending = pendingPassword
  pendingPassword = null
  return pending?.email === email ? pending.password : ''
}

export function clearRegistrationPassword(): void {
  pendingPassword = null
}
