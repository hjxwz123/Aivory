export interface ModelTier {
  id: string
  name: string
  /** Short marketing blurb shown in the model picker */
  blurb: string
  /** Capability tags. */
  capabilities: ('reasoning' | 'vision' | 'long-context' | 'fast' | 'creative' | 'code')[]
  /** Approximate response speed in mock chunks/sec */
  speed: 'instant' | 'fast' | 'measured' | 'deep'
  /** Tier display ("Standard", "Pro", "Max"). */
  tier: 'standard' | 'pro' | 'max'
  /** Whether the user's current plan unlocks it. */
  unlocked: boolean
}
