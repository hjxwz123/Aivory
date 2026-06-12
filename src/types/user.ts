export interface User {
  id: string
  name: string
  email: string
  avatarUrl?: string
  plan: 'free' | 'pro' | 'max'
  createdAt: number
}

export interface Session {
  user: User
  expiresAt: number
}
