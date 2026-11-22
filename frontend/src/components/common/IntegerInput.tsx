import React, { useEffect, useState } from 'react';
import { parseStrictInt } from '../../utils/numberInput';

export interface IntegerInputProps {
  id?: string;
  className?: string;
  value: number;
  min?: number;
  max?: number;
  disabled?: boolean;
  placeholder?: string;
  ariaLabel?: string;
  onChange: (next: number) => void;
}

/**
 * 受控整数输入框：允许用户在输入过程中暂态清空或自由键入（不自动回弹默认值、不强制步长倍数），
 * 仅在输入有效整数时同步外部数值状态，失焦时恢复合法规范文本。
 */
export const IntegerInput: React.FC<IntegerInputProps> = ({
  id,
  className,
  value,
  min,
  max,
  disabled,
  placeholder,
  ariaLabel,
  onChange,
}) => {
  const [draft, setDraft] = useState<string>(() => String(value));

  useEffect(() => {
    setDraft((prev) => (parseStrictInt(prev) === value ? prev : String(value)));
  }, [value]);

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const raw = e.target.value;
    setDraft(raw);
    const parsed = parseStrictInt(raw);
    if (parsed !== null) {
      onChange(parsed);
    }
  };

  const handleBlur = () => {
    const parsed = parseStrictInt(draft);
    setDraft(parsed === null ? String(value) : String(parsed));
  };

  return (
    <input
      id={id}
      type="number"
      inputMode="numeric"
      step={1}
      min={min}
      max={max}
      className={className}
      value={draft}
      disabled={disabled}
      placeholder={placeholder}
      aria-label={ariaLabel}
      onChange={handleChange}
      onBlur={handleBlur}
    />
  );
};

export default IntegerInput;
