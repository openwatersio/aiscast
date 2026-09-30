import { Search, X } from "lucide-react";
import { useId } from "react";

/** A rounded search field with its own clear button, as a maps app has at the top of its panel. */
export function SearchField({
  value,
  onChange,
  onSubmit,
  onFocus,
  placeholder,
}: {
  value: string;
  onChange(value: string): void;
  onSubmit?(value: string): void;
  onFocus?(): void;
  /** Also the field's accessible name. */
  placeholder: string;
}) {
  const id = useId();
  return (
    <form
      className="relative"
      role="search"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit?.(value.trim());
      }}
    >
      <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-muted" aria-hidden />
      <label className="sr-only" htmlFor={id}>
        {placeholder}
      </label>
      <input
        id={id}
        type="search"
        autoComplete="off"
        placeholder={placeholder}
        // 16px on phones: iOS Safari zooms the page into any field set smaller when it takes focus.
        className="w-full rounded-full border border-transparent bg-surface-tile py-2.5 pr-9 pl-9 text-base text-fg outline-none placeholder:text-fg-muted focus:border-accent md:text-body"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onFocus={onFocus}
      />
      {value && (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => onChange("")}
          className="absolute top-1/2 right-2 flex size-6 -translate-y-1/2 items-center justify-center rounded-full text-fg-muted hover:text-fg"
        >
          <X className="size-4" aria-hidden />
        </button>
      )}
    </form>
  );
}
