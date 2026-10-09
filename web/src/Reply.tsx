import Markdown, { type Components } from 'react-markdown'
import { textSegments, type Source } from './lib/toolChips'

const citePrefix = '#cite-'

export function Reply({ payload, cited }: { payload: unknown; cited: Source[] }) {
  const components: Components = {
    a: ({ href = '', children }) => {
      if (href.startsWith(citePrefix)) {
        const n = Number(href.slice(citePrefix.length))
        const source = cited[n - 1]
        if (!source) return null
        return (
          <sup className="ml-0.5 text-xs">
            <a
              href={source.url}
              target="_blank"
              rel="noopener noreferrer"
              title={source.title}
              aria-label={`Source ${n}: ${source.title}`}
              className="text-accent-ink focus-ring"
            >
              [{n}]
            </a>
          </sup>
        )
      }
      return (
        <a href={href} target="_blank" rel="noopener noreferrer" className="text-accent-ink underline focus-ring">
          {children}
        </a>
      )
    },
  }
  const markdown = textSegments(payload)
    .map((seg) => seg.text + seg.cites.map((n) => `[${n}](${citePrefix}${n})`).join(''))
    .join('')
  return (
    <div
      className="mr-auto [overflow-wrap:anywhere] [&>*+*]:mt-3 [&_ol]:list-decimal [&_ol]:pl-6 [&_ul]:list-disc [&_ul]:pl-6 [&_li>ul]:mt-1 [&_li>ol]:mt-1 [&_:is(h1,h2,h3,h4,h5,h6)]:font-semibold [&_:not(pre)>code]:rounded [&_:not(pre)>code]:bg-sunk [&_:not(pre)>code]:px-1 [&_code]:font-mono [&_code]:text-[0.9em] [&_pre]:overflow-x-auto [&_pre]:rounded-btn [&_pre]:border [&_pre]:border-line [&_pre]:bg-sunk [&_pre]:p-3"
    >
      <Markdown components={components}>{markdown}</Markdown>
    </div>
  )
}
