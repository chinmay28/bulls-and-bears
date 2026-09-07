import { api } from '../api'
import { TabPage } from '../components/Layout'
import { Empty, Loading, useLoader } from '../components/ui'

/** Positions, working orders and fills — the paper book, and later the live
 *  one beside it. Empty until the first run opens the book. */
export default function Book() {
  const { data, error, loading, offline } = useLoader(() => api.overview(), [], 10000)

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />
      {!data && loading && <div className="empty">Loading…</div>}
      {data && !data.book && (
        <Empty message="The book is empty. Positions, orders and fills appear here once a run places something in the paper account." />
      )}
    </TabPage>
  )
}
