import { Link } from 'react-router-dom'
import { Page } from '../components/Layout'
import { Empty } from '../components/ui'

export default function NotFound() {
  return (
    <Page title="Not found">
      <Empty
        message="That page doesn't exist."
        action={
          <Link to="/">
            <button className="primary">Go to overview</button>
          </Link>
        }
      />
    </Page>
  )
}
