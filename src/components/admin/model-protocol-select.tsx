import { useTranslation } from 'react-i18next'
import type { ApiModel, ApiModelProtocol } from '@/api/types'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { MODEL_PROTOCOLS, protocolForKind } from '@/lib/model-protocol'

export function ModelProtocolSelect({ id, kind, value, onChange, disabled, discovery = false }: {
  id: string
  kind?: ApiModel['kind']
  value?: ApiModelProtocol
  onChange: (value: ApiModelProtocol) => void
  disabled?: boolean
  discovery?: boolean
}) {
  const { t } = useTranslation('admin')
  const options = MODEL_PROTOCOLS.filter((option) => discovery
    ? ['openai.chat', 'openai.responses', 'anthropic.messages', 'gemini.generateContent', 'typesafe.decisions'].includes(option.value)
    : !kind || option.kinds.includes(kind))
  return (
    <Field label={t(discovery ? 'models.protocol.discovery' : 'models.protocol.label')} htmlFor={id}>
      <Select value={value ?? protocolForKind(kind ?? 'chat')} onValueChange={(next) => onChange(next as ApiModelProtocol)} disabled={disabled}>
        <SelectTrigger id={id}><SelectValue /></SelectTrigger>
        <SelectContent>
          {options.map((option) => <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>)}
        </SelectContent>
      </Select>
    </Field>
  )
}
